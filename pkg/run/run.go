package run

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/lmittmann/tint"

	"github.com/housecat-inc/spacecat/pkg/api"
	"github.com/housecat-inc/spacecat/pkg/code"
	"github.com/housecat-inc/spacecat/pkg/config"
	"github.com/housecat-inc/spacecat/pkg/deps"
	"github.com/housecat-inc/spacecat/pkg/pg"
	"github.com/housecat-inc/spacecat/pkg/watch"
)

const defaultSpacecatURL = "http://spacecat.localhost:50000"

// Run registers with the spacecat dashboard, watches for file changes,
// and manages a blue/green build/run cycle. It blocks until interrupted.
func Run(defaults ...map[string]string) {
	var defs map[string]string
	if len(defaults) > 0 {
		defs = defaults[0]
	}
	appEnv := config.Load(config.DefaultEnv(), ".envrc.example", defs)

	spacecatURL := envOr("SPACECAT_URL", defaultSpacecatURL)
	sp, err := code.Default()
	if err != nil {
		slog.Error("failed to determine space", "error", err)
		os.Exit(1)
	}

	logger := slog.New(tint.NewHandler(os.Stderr, &tint.Options{
		Level:      slog.LevelInfo,
		TimeFormat: time.Kitchen,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey {
				return tint.Attr(6, slog.String(slog.LevelKey, "RUN")) // cyan RUN label
			}
			return a
		},
	})).With("space", sp.Name)
	slog.SetDefault(logger)

	resp, err := register(spacecatURL, api.RegisterRequest{
		Space:         sp.Name,
		Dir:           sp.Dir,
		ConfigFile:    ".envrc",
		WatchPatterns: []string{".envrc", "*.go", "*.sql", "go.mod"},
	})
	if err != nil {
		logger.Error("failed to register", "error", err)
		os.Exit(1)
	}
	logger.Info("register", "port1", resp.Port1, "port2", resp.Port2)

	runner := &appRunner{
		appEnv:      appEnv,
		cmds:        make(map[int]*exec.Cmd),
		dir:         sp.Dir,
		logger:      logger,
		portActive:  resp.Port1,
		resp:        resp,
		space:       sp.Name,
		spacecatURL: spacecatURL,
	}

	// Ensure database (template + clone) before first build
	if err := runner.ensureDatabase(); err != nil {
		logger.Error("database setup failed", "error", err)
		os.Exit(1)
	}

	if err := config.Sync(config.DefaultConfig()); err != nil {
		logger.Warn("config sync failed", "error", err)
	}

	if err := deps.Sync(deps.DefaultConfig()); err != nil {
		logger.Warn("deps sync failed", "error", err)
	}

	if err := runner.buildAndStart(resp.Port1); err != nil {
		logger.Error("initial build failed", "error", err)
		os.Exit(1)
	}
	runner.updateHealth("unknown")

	runner.waitForHealthy(resp.Port1)
	runner.updateHealth("healthy")

	// File watcher
	w := watch.New(sp.Dir, []string{".envrc", "*.go", "*.sql", "go.mod"}, nil, func(path string) {
		runner.rebuild(path)
	})
	w.Start()

	// Wait for signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down")
	w.Stop()
	runner.stopAll()
	deregister(spacecatURL, sp.Name)
}

type appRunner struct {
	appEnv      map[string]string
	cmds        map[int]*exec.Cmd
	dir         string
	logger      *slog.Logger
	mu          sync.Mutex
	portActive  int
	resp        *api.RegisterResponse
	space       string
	spacecatURL string
}

func (r *appRunner) buildAndStart(port int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Build into .spacecat/ (gitignored, watcher-ignored)
	binPath := filepath.Join(".spacecat", "app")
	os.MkdirAll(".spacecat", 0o755)

	build := exec.Command("go", "build", "-o", binPath, "./cmd/app")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	build.Env = append(os.Environ(),
		fmt.Sprintf("DATABASE_URL=%s", r.resp.DatabaseURL),
	)
	if err := build.Run(); err != nil {
		return errors.Wrap(err, "build")
	}

	cmd := exec.Command(binPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	for k, v := range r.appEnv {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = append(cmd.Env,
		fmt.Sprintf("PORT=%d", port),
		fmt.Sprintf("DATABASE_URL=%s", r.resp.DatabaseURL),
		fmt.Sprintf("SPACE=%s", r.space),
	)
	if err := cmd.Start(); err != nil {
		return errors.Wrap(err, "run")
	}

	r.cmds[port] = cmd

	r.logger.Info("server", "port", port, "pid", cmd.Process.Pid)
	return nil
}

// rebuild does a zero-downtime blue/green deploy:
// 1. Run pre-build hooks based on changed file
// 2. Build + start on the inactive color
// 3. Wait for the new process to be healthy
// 4. Swap activeColor (proxy switches)
// 5. Stop the old process
func (r *appRunner) rebuild(changedPath string) {
	if rel, err := filepath.Rel(r.dir, changedPath); err == nil {
		changedPath = rel
	}
	r.logger.Info("builder")

	if filepath.Base(changedPath) == ".envrc" {
		if err := config.Sync(config.DefaultConfig()); err != nil {
			r.logger.Error("config sync failed", "error", err)
			r.sendLog("error", fmt.Sprintf("config sync failed: %v", err))
			return
		}
	}

	if filepath.Base(changedPath) == "go.mod" {
		if err := deps.Sync(deps.DefaultConfig()); err != nil {
			r.logger.Error("deps sync failed", "error", err)
			r.sendLog("error", fmt.Sprintf("deps sync failed: %v", err))
			return
		}
	}

	if strings.HasSuffix(changedPath, ".sql") {
		r.logger.Info("migrator", "path", changedPath)
		if err := r.ensureDatabase(); err != nil {
			r.logger.Error("database rebuild failed", "error", err)
			r.sendLog("error", fmt.Sprintf("database rebuild failed: %v", err))
			return
		}
		if pg.HasSqlcConfig(".") {
			gen := exec.Command("go", "generate", "./...")
			gen.Stdout = os.Stdout
			gen.Stderr = os.Stderr
			if err := gen.Run(); err != nil {
				r.logger.Warn("go generate failed", "error", err)
			}
		}
	}

	r.mu.Lock()
	oldPort := r.portActive
	newPort := r.resp.Port2
	if oldPort == r.resp.Port2 {
		newPort = r.resp.Port1
	}
	r.mu.Unlock()

	if err := r.buildAndStart(newPort); err != nil {
		r.logger.Error("build failed", "error", err)
		r.sendLog("error", fmt.Sprintf("build failed: %v", err))
		return
	}

	if !r.waitForHealthy(newPort) {
		r.logger.Error("health check failed, aborting swap")
		r.sendLog("error", "health check failed")
		r.stopPort(newPort)
		return
	}

	r.mu.Lock()
	r.portActive = newPort
	r.mu.Unlock()
	r.updateHealth("healthy")

	r.stopPort(oldPort)
}

// waitForHealthy polls the health endpoint until healthy or timeout.
func (r *appRunner) waitForHealthy(port int) bool {
	client := &http.Client{Timeout: 1 * time.Second}
	url := fmt.Sprintf("http://localhost:%d/health", port)

	for i := 0; i < 30; i++ { // 30 * 500ms = 15s max
		time.Sleep(500 * time.Millisecond)
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
	}
	return false
}

func (r *appRunner) stopPort(port int) {
	r.mu.Lock()
	cmd := r.cmds[port]
	delete(r.cmds, port)
	r.mu.Unlock()

	stopProcess(cmd)
}

func (r *appRunner) stopAll() {
	r.mu.Lock()
	cmds := r.cmds
	r.cmds = make(map[int]*exec.Cmd)
	r.mu.Unlock()

	for _, cmd := range cmds {
		stopProcess(cmd)
	}
}

// ensureDatabase discovers migrations, hashes them, creates/updates the
// template DB, and clones it to the app's database.
func (r *appRunner) ensureDatabase() error {
	migDir, err := pg.MigrationDir(".")
	if err != nil {
		return nil // no migrations, skip silently
	}

	hash, err := pg.Hash(migDir)
	if err != nil {
		return errors.Wrap(err, "hash migrations")
	}

	adminURL, err := pg.AdminURL(r.resp.DatabaseURL)
	if err != nil {
		return errors.Wrap(err, "admin url")
	}

	tmplName, err := pg.Template(adminURL, migDir, hash)
	if err != nil {
		return errors.Wrap(err, "ensure template")
	}

	appDBName, err := pg.DBName(r.resp.DatabaseURL)
	if err != nil {
		return errors.Wrap(err, "db name")
	}

	if err := pg.Create(adminURL, tmplName, appDBName); err != nil {
		return errors.Wrap(err, "clone db")
	}

	r.logger.Info("database", "template", tmplName, "database_url", r.resp.DatabaseURL)
	return nil
}

func stopProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}

	cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		<-done
	}
}

func (r *appRunner) updateHealth(status string) {
	r.mu.Lock()
	port := r.portActive
	r.mu.Unlock()

	body, _ := json.Marshal(map[string]any{
		"status":      status,
		"port_active": port,
	})
	url := fmt.Sprintf("%s/api/apps/%s/health", r.spacecatURL, r.space)
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	http.DefaultClient.Do(req)
}

func (r *appRunner) sendLog(level, message string) {
	entries := []api.LogEntry{{
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
	}}
	body, _ := json.Marshal(entries)
	url := fmt.Sprintf("%s/api/apps/%s/logs", r.spacecatURL, r.space)
	http.Post(url, "application/json", bytes.NewReader(body))
}

func register(spacecatURL string, req api.RegisterRequest) (*api.RegisterResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, errors.Wrap(err, "marshal")
	}
	resp, err := http.Post(spacecatURL+"/api/apps", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, errors.Wrap(err, "post")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, errors.Newf("register failed: %s", resp.Status)
	}
	var result api.RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, errors.Wrap(err, "decode")
	}
	return &result, nil
}

func deregister(spacecatURL, space string) {
	req, _ := http.NewRequest(http.MethodDelete, spacecatURL+"/api/apps/"+space, nil)
	http.DefaultClient.Do(req)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
