package main

import (
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/lmittmann/tint"

	"github.com/housecat-inc/spacecat/pkg/watch"
)

func main() {
	logger := slog.New(tint.NewHandler(os.Stderr, &tint.Options{
		Level:      slog.LevelInfo,
		TimeFormat: time.Kitchen,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey {
				return tint.Attr(5, slog.String(slog.LevelKey, "DEV")) // magenta DEV label
			}
			return a
		},
	}))
	slog.SetDefault(logger)

	runner := &proxyRunner{logger: logger}
	if err := runner.start(); err != nil {
		logger.Error("failed to start proxy", "error", err)
		os.Exit(1)
	}

	// Watch spacecat source files, ignoring child apps
	cwd, _ := os.Getwd()
	var (
		restartTimer *time.Timer
		timerMu      sync.Mutex
	)

	w := watch.New(cwd, []string{"*.go"}, []string{"apps"}, func(path string) {
		timerMu.Lock()
		defer timerMu.Unlock()
		if restartTimer != nil {
			restartTimer.Stop()
		}
		restartTimer = time.AfterFunc(500*time.Millisecond, func() {
			rel := path
			if r, err := filepath.Rel(cwd, path); err == nil {
				rel = r
			}
			logger.Info("restart", "path", rel)
			runner.restart()
		})
	})
	w.Start()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down")
	w.Stop()
	runner.stop()
	logger.Info("shutdown complete")
}

var binPath = filepath.Join(".spacecat", "spacecat")

type proxyRunner struct {
	logger *slog.Logger
	cmd    *exec.Cmd
	done   chan struct{} // closed when cmd.Wait() returns
	mu     sync.Mutex
}

func (r *proxyRunner) start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buildAndStartLocked()
}

func (r *proxyRunner) buildAndStartLocked() error {
	os.MkdirAll(".spacecat", 0o755)

	// Build the binary directly — avoids the go run wrapper process
	// which doesn't forward signals to its child
	build := exec.Command("go", "build", "-o", binPath, "./cmd/spacecat")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return errors.Wrap(err, "build")
	}

	cmd := exec.Command(binPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return errors.Wrap(err, "start")
	}

	r.cmd = cmd
	r.done = make(chan struct{})
	go func() {
		cmd.Wait()
		close(r.done)
	}()

	r.logger.Info("proxy", "pid", cmd.Process.Pid)
	return nil
}

func (r *proxyRunner) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopLocked()
}

func (r *proxyRunner) stopLocked() {
	if r.cmd == nil || r.cmd.Process == nil {
		return
	}
	r.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		r.cmd.Process.Kill()
		<-r.done
	}
	r.cmd = nil
}

func (r *proxyRunner) restart() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopLocked()
	if err := r.buildAndStartLocked(); err != nil {
		r.logger.Error("failed to restart proxy", "error", err)
	}
}
