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

	"github.com/lmittmann/tint"

	"github.com/housecat-inc/spacecat/pkg/api"
	"github.com/housecat-inc/spacecat/pkg/db"
	"github.com/housecat-inc/spacecat/pkg/watch"
)

const defaultSpacecatURL = "http://spacecat.localhost:50000"

// Run registers with the spacecat dashboard, watches for file changes,
// and manages a blue/green build/run cycle. It blocks until interrupted.
func Run() {
	spacecatURL := envOr("SPACECAT_URL", defaultSpacecatURL)
	space, err := determineSpace()
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
	})).With("space", space)
	slog.SetDefault(logger)

	dir, _ := os.Getwd()
	resp, err := register(spacecatURL, api.RegisterRequest{
		Space:         space,
		Dir:           dir,
		ConfigFile:    ".envrc",
		WatchPatterns: []string{"*.go", "go.mod", "*.sql"},
	})
	if err != nil {
		logger.Error("failed to register", "error", err)
		os.Exit(1)
	}
	logger.Info("register", "port1", resp.Port1, "port2", resp.Port2)

	runner := &appRunner{
		spacecatURL: spacecatURL,
		space:       space,
		resp:        resp,
		activeColor: "blue",
		logger:      logger,
	}

	// Ensure database (template + clone) before first build
	if err := runner.ensureDatabase(); err != nil {
		logger.Error("database setup failed", "error", err)
		os.Exit(1)
	}

	// Run go generate if sqlc config exists
	if db.HasSqlcConfig(".") {
		gen := exec.Command("go", "generate", "./...")
		gen.Stdout = os.Stdout
		gen.Stderr = os.Stderr
		if err := gen.Run(); err != nil {
			logger.Warn("go generate failed", "error", err)
		}
	}

	// Initial build + run on blue
	if err := runner.buildAndStart("blue"); err != nil {
		logger.Error("initial build failed", "error", err)
		os.Exit(1)
	}
	runner.updateHealth("unknown")

	// Wait for initial health before announcing
	runner.waitForHealthy(runner.portForColor("blue"))
	runner.updateHealth("healthy")

	// File watcher
	w := watch.New(dir, []string{"*.go", "go.mod", "*.sql"}, nil, func(path string) {
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
	deregister(spacecatURL, space)
}

type appRunner struct {
	spacecatURL string
	space       string
	resp        *api.RegisterResponse
	activeColor string
	blueCmd     *exec.Cmd
	greenCmd    *exec.Cmd
	mu          sync.Mutex
	logger      *slog.Logger
}

func (r *appRunner) portForColor(color string) int {
	if color == "green" {
		return r.resp.Port2
	}
	return r.resp.Port1
}

// buildAndStart builds the binary and starts it on the given color's port.
// Does NOT stop any existing process or change activeColor.
func (r *appRunner) buildAndStart(color string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	port := r.portForColor(color)

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
		return fmt.Errorf("build: %w", err)
	}

	// Run
	cmd := exec.Command(binPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		fmt.Sprintf("DATABASE_URL=%s", r.resp.DatabaseURL),
		fmt.Sprintf("SPACE=%s", r.space),
	)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("run: %w", err)
	}

	if color == "green" {
		r.greenCmd = cmd
	} else {
		r.blueCmd = cmd
	}

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
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, changedPath); err == nil {
			changedPath = rel
		}
	}
	r.logger.Info("builder")

	// Pre-build hooks
	if filepath.Base(changedPath) == "go.mod" {
		tidy := exec.Command("go", "mod", "tidy")
		tidy.Stdout = os.Stdout
		tidy.Stderr = os.Stderr
		if err := tidy.Run(); err != nil {
			r.logger.Error("go mod tidy failed", "error", err)
			r.sendLog("error", fmt.Sprintf("go mod tidy failed: %v", err))
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
		if db.HasSqlcConfig(".") {
			gen := exec.Command("go", "generate", "./...")
			gen.Stdout = os.Stdout
			gen.Stderr = os.Stderr
			if err := gen.Run(); err != nil {
				r.logger.Warn("go generate failed", "error", err)
			}
		}
	}

	r.mu.Lock()
	oldColor := r.activeColor
	newColor := "green"
	if oldColor == "green" {
		newColor = "blue"
	}
	r.mu.Unlock()

	// Build + start new
	if err := r.buildAndStart(newColor); err != nil {
		r.logger.Error("build failed", "error", err)
		r.sendLog("error", fmt.Sprintf("build failed: %v", err))
		return
	}

	// Wait for new to be healthy
	newPort := r.portForColor(newColor)
	if !r.waitForHealthy(newPort) {
		r.logger.Error("health check failed, aborting swap")
		r.sendLog("error", "health check failed")
		r.stopColor(newColor)
		return
	}

	// Swap — proxy now routes to the new color
	r.mu.Lock()
	r.activeColor = newColor
	r.mu.Unlock()
	r.updateHealth("healthy")

	// Stop old
	r.stopColor(oldColor)
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

func (r *appRunner) stopColor(color string) {
	r.mu.Lock()
	var cmd *exec.Cmd
	if color == "green" {
		cmd = r.greenCmd
		r.greenCmd = nil
	} else {
		cmd = r.blueCmd
		r.blueCmd = nil
	}
	r.mu.Unlock()

	stopProcess(cmd)
}

func (r *appRunner) stopAll() {
	r.mu.Lock()
	blue := r.blueCmd
	green := r.greenCmd
	r.blueCmd = nil
	r.greenCmd = nil
	r.mu.Unlock()

	stopProcess(blue)
	stopProcess(green)
}

// ensureDatabase discovers migrations, hashes them, creates/updates the
// template DB, and clones it to the app's database.
func (r *appRunner) ensureDatabase() error {
	migDir, err := db.FindMigrationDir(".")
	if err != nil {
		return nil // no migrations, skip silently
	}

	hash, err := db.HashMigrations(migDir)
	if err != nil {
		return fmt.Errorf("hash migrations: %w", err)
	}

	adminURL, err := db.AdminURL(r.resp.DatabaseURL)
	if err != nil {
		return fmt.Errorf("admin url: %w", err)
	}

	tmplName, err := db.EnsureTemplate(adminURL, migDir, hash)
	if err != nil {
		return fmt.Errorf("ensure template: %w", err)
	}

	appDBName, err := db.DBNameFromURL(r.resp.DatabaseURL)
	if err != nil {
		return fmt.Errorf("db name: %w", err)
	}

	if err := db.CloneDB(adminURL, tmplName, appDBName); err != nil {
		return fmt.Errorf("clone db: %w", err)
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
	color := r.activeColor
	r.mu.Unlock()

	body, _ := json.Marshal(map[string]string{
		"status":       status,
		"active_color": color,
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

// Helpers

func determineSpace() (string, error) {
	if space := os.Getenv("SPACE"); space != "" {
		return space, nil
	}
	if space := os.Getenv("CONDUCTOR_WORKSPACE_NAME"); space != "" {
		return space, nil
	}
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("set SPACE or CONDUCTOR_WORKSPACE_NAME env var, or run in a git repo")
	}
	branch := strings.TrimSpace(string(out))
	branch = strings.ReplaceAll(branch, "/", "-")
	return branch, nil
}

func register(spacecatURL string, req api.RegisterRequest) (*api.RegisterResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := http.Post(spacecatURL+"/api/apps", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("register failed: %s", resp.Status)
	}
	var result api.RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
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
