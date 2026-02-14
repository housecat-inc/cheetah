package main

import (
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/housecat-inc/spacecat/pkg/watch"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	runner := &proxyRunner{logger: logger}
	runner.start()

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
			logger.Info("source changed, restarting proxy", "path", path)
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

type proxyRunner struct {
	logger *slog.Logger
	cmd    *exec.Cmd
	mu     sync.Mutex
}

func (r *proxyRunner) start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startLocked()
}

func (r *proxyRunner) startLocked() {
	cmd := exec.Command("go", "run", "./cmd/spacecat")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		r.logger.Error("failed to start proxy", "error", err)
		return
	}
	r.cmd = cmd
	r.logger.Info("proxy started", "pid", cmd.Process.Pid)
	go cmd.Wait()
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
	done := make(chan struct{})
	go func() {
		r.cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		r.cmd.Process.Kill()
		<-done
	}
	r.cmd = nil
}

func (r *proxyRunner) restart() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopLocked()
	r.startLocked()
}
