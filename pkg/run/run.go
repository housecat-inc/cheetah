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

	// Initial build + run
	if err := runner.buildAndRun(); err != nil {
		logger.Error("initial build failed", "error", err)
		os.Exit(1)
	}

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
	runner.stopCurrent()
	deregister(spacecatURL, space)
}

type appRunner struct {
	spacecatURL string
	space       string
	resp        *api.RegisterResponse
	activeColor string
	currentCmd  *exec.Cmd
	mu          sync.Mutex
	logger      *slog.Logger
}

func (r *appRunner) activePort() int {
	if r.activeColor == "green" {
		return r.resp.GreenPort
	}
	return r.resp.BluePort
}

func (r *appRunner) buildAndRun() error {
	r.mu.Lock()
	defer r.mu.Unlock()

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

	port := r.activePort()

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

	r.currentCmd = cmd
	r.logger.Info("app started", "port", port, "color", r.activeColor, "pid", cmd.Process.Pid)

	// Health check in background
	go r.healthCheckLoop(port)

	return nil
}

func (r *appRunner) rebuild() {
	r.mu.Lock()
	if r.activeColor == "blue" {
		r.activeColor = "green"
	} else {
		r.activeColor = "blue"
	}
	r.mu.Unlock()

	r.stopCurrent()

	if err := r.buildAndRun(); err != nil {
		r.logger.Error("rebuild failed", "error", err)
		r.sendLog("error", fmt.Sprintf("rebuild failed: %v", err))
		// Swap back
		r.mu.Lock()
		if r.activeColor == "blue" {
			r.activeColor = "green"
		} else {
			r.activeColor = "blue"
		}
		r.mu.Unlock()
	}
}

func (r *appRunner) stopCurrent() {
	r.mu.Lock()
	cmd := r.currentCmd
	r.currentCmd = nil
	r.mu.Unlock()

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

func (r *appRunner) healthCheckLoop(port int) {
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://localhost:%d/health", port)

	for {
		time.Sleep(5 * time.Second)

		r.mu.Lock()
		if r.currentCmd == nil || r.activePort() != port {
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()

		resp, err := client.Get(url)
		status := "unhealthy"
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				status = "healthy"
			}
		}

		r.updateHealth(status)
	}
}

func (r *appRunner) updateHealth(status string) {
	body, _ := json.Marshal(map[string]string{"status": status})
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
	resp, err := http.Post(spacecatURL+"/api/apps", "application/json", bytes.NewReader(body))
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
	req, _ := http.NewRequest(http.MethodDelete, spacecatURL+"/api/apps/"+space, nil)
	http.DefaultClient.Do(req)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
