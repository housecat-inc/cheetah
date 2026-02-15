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

	"github.com/housecat-inc/spacecat/pkg/api"
	"github.com/housecat-inc/spacecat/pkg/config"
	"github.com/housecat-inc/spacecat/pkg/pg"
)

var (
	bluePortStart = config.EnvOr("APP_PORT", 4000)
	dashboardPort = config.EnvOr("PORT", 50000)
	postgresPort  = config.EnvOr("PG_PORT", 54320)
)

func main() {
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

	stateFile := filepath.Join(".spacecat", "state.json")
	os.MkdirAll(".spacecat", 0o755)
	srv.LoadState(stateFile)

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	srv.Middleware(e)
	srv.Routes(e)

	go srv.PeriodicSave(stateFile, 5*time.Second)

	go func() {
		addr := fmt.Sprintf(":%d", dashboardPort)
		logger.Info("spacecat", "url", fmt.Sprintf("http://spacecat.localhost:%d", dashboardPort))
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.Shutdown(ctx); err != nil {
		logger.Error("server shutdown error", "error", err)
	}
	srv.SaveState(stateFile)
	pg.Stop(postgresPort)
	logger.Info("shutdown complete")
}
