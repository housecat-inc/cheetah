package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
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

func usage() {
	fmt.Fprintf(os.Stderr, `cheetah %s — development dashboard

Usage:
  cheetah [flags] [command]

Commands:
  status    Show cheetah and postgres status
  stop      Stop the running cheetah daemon
  update    Update cheetah to the latest version
  version   Print version

Flags:
  -h, --help      Show this help
  -v, --version   Print version
`, version.Get())
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			usage()
			return
		case "-v", "--version", "version":
			fmt.Println(version.Get())
			return
		case "status":
			status()
			return
		case "stop":
			stop()
			return
		case "update":
			update()
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
			usage()
			os.Exit(1)
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

func status() {
	url := fmt.Sprintf("http://localhost:%d/api/status", dashboardPort)
	client := &http.Client{Timeout: time.Second}

	resp, err := client.Get(url)
	if err != nil {
		fmt.Printf("cheetah:  stopped\n")
		fmt.Printf("postgres: %s\n", pgStatus())
		fmt.Printf("version:  %s\n", version.Get())
		return
	}
	defer resp.Body.Close()

	var s api.Status
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		fmt.Printf("cheetah:  running (http://localhost:%d)\n", dashboardPort)
		fmt.Printf("postgres: unknown\n")
		fmt.Printf("version:  %s\n", version.Get())
		return
	}

	fmt.Printf("cheetah:  running (http://localhost:%d)\n", dashboardPort)
	if s.PostgresRunning {
		fmt.Printf("postgres: running (localhost:%d)\n", s.PostgresPort)
	} else {
		fmt.Printf("postgres: stopped\n")
	}
	fmt.Printf("apps:     %d\n", s.AppCount)
	fmt.Printf("uptime:   %s\n", s.Uptime)
	fmt.Printf("version:  %s\n", s.Version)
}

func pgStatus() string {
	if pg.Dial() {
		return fmt.Sprintf("running (localhost:%d)", postgresPort)
	}
	return "stopped"
}

func stop() {
	pid := findPID()
	if pid == 0 {
		fmt.Fprintln(os.Stderr, "cheetah is not running")
		os.Exit(1)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cheetah is not running")
		os.Exit(1)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintln(os.Stderr, "cheetah is not running")
		os.Exit(1)
	}

	fmt.Println("cheetah stopped")
}

func findPID() int {
	home, _ := os.UserHomeDir()
	pidFile := filepath.Join(home, ".cheetah", "cheetah.pid")
	if data, err := os.ReadFile(pidFile); err == nil {
		var pid int
		fmt.Sscanf(string(data), "%d", &pid)
		if pid > 0 {
			if proc, err := os.FindProcess(pid); err == nil {
				if err := proc.Signal(syscall.Signal(0)); err == nil {
					return pid
				}
			}
		}
	}

	out, err := exec.Command("lsof", "-ti", fmt.Sprintf(":%d", dashboardPort)).Output()
	if err != nil {
		return 0
	}
	var pid int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &pid)
	return pid
}

func update() {
	fmt.Println("updating cheetah...")
	cmd := exec.Command("go", "install", "github.com/housecat-inc/cheetah/cmd/cheetah@latest")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "update failed: %s\n", err)
		os.Exit(1)
	}
	fmt.Println("cheetah updated to latest version")
}
