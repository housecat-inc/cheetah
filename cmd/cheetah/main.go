package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lmittmann/tint"

	"github.com/housecat-inc/cheetah/pkg/api"
	"github.com/housecat-inc/cheetah/pkg/config"
	"github.com/housecat-inc/cheetah/pkg/pg"
	"github.com/housecat-inc/cheetah/pkg/version"
)

var (
	bluePortStart = config.EnvOr("APP_PORT", 4000)
	dashboardPort = config.EnvOr("PORT", 50000)
	postgresPort  = config.EnvOr("PG_PORT", 54320)
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "stop":
			stop()
			return
		case "version":
			fmt.Println(version.Get())
			return
		}
	}

	logger := slog.New(tint.NewHandler(os.Stderr, &tint.Options{Level: slog.LevelInfo, TimeFormat: time.Kitchen}))
	slog.SetDefault(logger)

	srv := api.NewServer(api.ServerConfig{
		BluePortStart: bluePortStart,
		DashboardPort: dashboardPort,
		PostgresPort:  postgresPort,
	}, logger)

	pgURL, err := pg.Run()
	if err != nil {
		logger.Error("failed to ensure postgres", "error", err)
		os.Exit(1)
	}
	srv.SetPostgres(true, pgURL)

	home, _ := os.UserHomeDir()
	cheetahDir := filepath.Join(home, ".cheetah")
	os.MkdirAll(cheetahDir, 0o755)
	pidFile := filepath.Join(cheetahDir, "cheetah.pid")
	os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644)
	stateFile := filepath.Join(cheetahDir, "state.json")
	srv.LoadState(stateFile)

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	srv.Middleware(e)
	srv.Routes(e)

	go srv.PeriodicSave(stateFile, 5*time.Second)

	startErr := make(chan error, 1)
	go func() {
		addr := fmt.Sprintf(":%d", dashboardPort)
		logger.Info("cheetah", "url", fmt.Sprintf("http://localhost:%d", dashboardPort))
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			startErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-quit:
	case <-startErr:
	}
	logger.Info("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.Shutdown(ctx); err != nil {
		logger.Error("server shutdown error", "error", err)
	}
	srv.SaveState(stateFile)
	os.Remove(pidFile)
	pg.Stop(postgresPort)
	logger.Info("shutdown complete")
}

func stop() {
	home, _ := os.UserHomeDir()
	pidFile := filepath.Join(home, ".cheetah", "cheetah.pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cheetah is not running")
		os.Exit(1)
	}

	var pid int
	fmt.Sscanf(string(data), "%d", &pid)

	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cheetah is not running")
		os.Remove(pidFile)
		os.Exit(1)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintln(os.Stderr, "cheetah is not running")
		os.Remove(pidFile)
		os.Exit(1)
	}

	fmt.Println("cheetah stopped")
}
