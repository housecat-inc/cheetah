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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/housecat-inc/spacecat/pkg/api"
	"github.com/housecat-inc/spacecat/pkg/watch"
)

const defaultSpacecatURL = "http://localhost:8080"

// Run registers with the spacecat dashboard, watches for file changes,
// and manages a blue/green build/run cycle. It blocks until interrupted.
func Run() {
	logger := slog.Default()

	spacecatURL := envOr("SPACECAT_URL", defaultSpacecatURL)
	space, err := determineSpace()
	if err != nil {
		logger.Error("failed to determine space", "error", err)
		os.Exit(1)
	}

	logger.Info("registering with spacecat", "space", space, "url", spacecatURL)
	resp, err := register(spacecatURL, api.RegisterRequest{
		Space:         space,
		ConfigFile:    ".envrc",
		WatchPatterns: []string{"*.go"},
	})
	if err != nil {
		logger.Error("failed to register", "error", err)
		os.Exit(1)
	}
	logger.Info("registered",
		"proxy_port", resp.ProxyPort,
		"blue_port", resp.BluePort,
		"green_port", resp.GreenPort,
		"database_url", resp.DatabaseURL,
	)

	runner := &appRunner{
		spacecatURL: spacecatURL,
		space:       space,
		resp:        resp,
		activeColor: "blue",
		logger:      logger,
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
	dir, _ := os.Getwd()
	w := watch.New(dir, []string{"*.go"}, nil, func(path string) {
		logger.Info("file changed, rebuilding", "path", path)
		runner.rebuild()
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
		return r.resp.GreenPort
	}
	return r.resp.BluePort
}

// buildAndStart builds the binary and starts it on the given color's port.
// Does NOT stop any existing process or change activeColor.
func (r *appRunner) buildAndStart(color string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	port := r.portForColor(color)

	// Build
	build := exec.Command("go", "build", "-o", "./app", "./cmd/app")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	build.Env = append(os.Environ(),
		fmt.Sprintf("DATABASE_URL=%s", r.resp.DatabaseURL),
		fmt.Sprintf("DATABASE_TEMPLATE_URL=%s", r.resp.TemplateDBURL),
	)
	if err := build.Run(); err != nil {
		return fmt.Errorf("build: %w", err)
	}

	// Run
	cmd := exec.Command("./app")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		fmt.Sprintf("DATABASE_URL=%s", r.resp.DatabaseURL),
		fmt.Sprintf("DATABASE_TEMPLATE_URL=%s", r.resp.TemplateDBURL),
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

	r.logger.Info("app started", "port", port, "color", color, "pid", cmd.Process.Pid)
	return nil
}

// rebuild does a zero-downtime blue/green deploy:
// 1. Build + start on the inactive color
// 2. Wait for the new process to be healthy
// 3. Swap activeColor (proxy switches)
// 4. Stop the old process
func (r *appRunner) rebuild() {
	r.mu.Lock()
	oldColor := r.activeColor
	newColor := "green"
	if oldColor == "green" {
		newColor = "blue"
	}
	r.mu.Unlock()

	r.logger.Info("blue/green deploy", "old", oldColor, "new", newColor)

	// Build + start new
	if err := r.buildAndStart(newColor); err != nil {
		r.logger.Error("rebuild failed", "error", err)
		r.sendLog("error", fmt.Sprintf("rebuild failed: %v", err))
		return
	}

	// Wait for new to be healthy
	newPort := r.portForColor(newColor)
	if !r.waitForHealthy(newPort) {
		r.logger.Error("new process failed health check, aborting swap")
		r.sendLog("error", "new process failed health check")
		r.stopColor(newColor)
		return
	}

	// Swap — proxy now routes to the new color
	r.mu.Lock()
	r.activeColor = newColor
	r.mu.Unlock()
	r.updateHealth("healthy")
	r.logger.Info("swapped", "active", newColor, "port", newPort)

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
	url := fmt.Sprintf("%s/_spaces/api/apps/%s/health", r.spacecatURL, r.space)
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
	url := fmt.Sprintf("%s/_spaces/api/apps/%s/logs", r.spacecatURL, r.space)
	http.Post(url, "application/json", bytes.NewReader(body))
}

// Helpers

func determineSpace() (string, error) {
	if space := os.Getenv("SPACE"); space != "" {
		return space, nil
	}
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("set SPACE env var or run in a git repo")
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
	resp, err := http.Post(spacecatURL+"/_spaces/api/apps", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("register failed: %s", resp.Status)
	}
	var result api.RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func deregister(spacecatURL, space string) {
	req, _ := http.NewRequest(http.MethodDelete, spacecatURL+"/_spaces/api/apps/"+space, nil)
	http.DefaultClient.Do(req)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
