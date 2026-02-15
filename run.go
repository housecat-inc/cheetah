package cheetah

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/housecat-inc/cheetah/pkg/api"
	"github.com/housecat-inc/cheetah/pkg/build"
	"github.com/housecat-inc/cheetah/pkg/code"
	"github.com/housecat-inc/cheetah/pkg/config"
	"github.com/housecat-inc/cheetah/pkg/deps"
	"github.com/housecat-inc/cheetah/pkg/logs"
	"github.com/housecat-inc/cheetah/pkg/pg"
	"github.com/housecat-inc/cheetah/pkg/port"
	"github.com/housecat-inc/cheetah/pkg/watch"
)

const defaultURL = "http://cheetah.localhost:50000"

func Run(defaults ...map[string]string) {
	url := config.EnvOr("CHEETAH_URL", defaultURL)
	space, err := code.System()
	if err != nil {
		slog.Error("failed to determine space", "error", err)
		os.Exit(1)
	}

	cfg := config.Load(config.DefaultEnv(), space.Dir, defaults...)

	l := logs.New(space.Name)
	slog.SetDefault(l)

	client := api.NewClient(url)
	resp, err := client.AppPost(api.AppIn{
		Config: cfg.Providers,
		Dir:    space.Dir,
		Space:  space.Name,
		Watch:  api.Watch{Match: []string{".envrc", "*.go", "*.sql", "*.templ", "go.mod"}},
	})
	if err != nil {
		l.Error("failed to register", "error", err)
		os.Exit(1)
	}
	l.Info("register", "blue", resp.Ports.Blue, "green", resp.Ports.Green)

	ports := port.New(resp.Ports.Blue, resp.Ports.Green, port.DefaultConfig(client, space.Name))

	runner := &appRunner{
		appEnv:      cfg.Env,
		client:      client,
		cmds:        make(map[int]*exec.Cmd),
		defaults:    defaults,
		dir:         space.Dir,
		logger:      l,
		ports:       ports,
		resp:        resp,
		space:       space.Name,
		cheetahURL: url,
	}

	if err := pg.Ensure(resp.DatabaseURL); err != nil {
		l.Error("database setup failed", "error", err)
		os.Exit(1)
	}

	if err := config.Sync(config.DefaultConfig(), space.Dir); err != nil {
		l.Warn("config sync failed", "error", err)
	}

	if err := deps.Sync(deps.DefaultConfig()); err != nil {
		l.Warn("deps sync failed", "error", err)
	}

	if err := runner.start(resp.Ports.Blue); err != nil {
		l.Error("initial build failed", "error", err)
		os.Exit(1)
	}

	ports.ReportHealth("unknown")
	ports.WaitForHealthy(resp.Ports.Blue)
	ports.ReportHealth("healthy")

	w := watch.New(space.Dir, []string{".envrc", "*.go", "*.sql", "*.templ", "go.mod"}, nil, func(path string) {
		runner.rebuild(path)
	})
	w.Start()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	l.Info("shutting down")
	w.Stop()
	runner.stopAll()
	client.AppDelete(space.Name)
}

type appRunner struct {
	appEnv      map[string]string
	client      *api.Client
	cmds        map[int]*exec.Cmd
	defaults    []map[string]string
	dir         string
	logger      *slog.Logger
	mu          sync.Mutex
	ports       *port.Manager
	resp        *api.AppOut
	space       string
	cheetahURL string
}

func (r *appRunner) start(port int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	out, err := build.Run(build.In{
		AppEnv:      r.appEnv,
		DatabaseURL: r.resp.DatabaseURL,
		Port:        port,
		Space:       r.space,
		CheetahURL: r.cheetahURL,
	})
	if err != nil {
		return err
	}

	r.cmds[port] = out.Cmd
	return nil
}

func (r *appRunner) rebuild(changedPath string) {
	if rel, err := filepath.Rel(r.dir, changedPath); err == nil {
		changedPath = rel
	}
	r.logger.Info("builder")

	if filepath.Base(changedPath) == ".envrc" {
		if err := config.Sync(config.DefaultConfig(), r.dir); err != nil {
			r.logger.Error("config sync failed", "error", err)
			r.sendLog("error", fmt.Sprintf("config sync failed: %v", err))
			return
		}
		cfg := config.Load(config.DefaultEnv(), r.dir, r.defaults...)
		r.appEnv = cfg.Env
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
		if err := pg.Ensure(r.resp.DatabaseURL); err != nil {
			r.logger.Error("database rebuild failed", "error", err)
			r.sendLog("error", fmt.Sprintf("database rebuild failed: %v", err))
			return
		}
	}

	if !r.ports.Swap(r.start, r.stopPort) {
		r.logger.Error("swap failed")
		r.sendLog("error", "swap failed")
	}
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

func (r *appRunner) sendLog(level, message string) {
	r.client.LogPost(r.space, []api.Log{{
		Level:     level,
		Message:   message,
		Timestamp: time.Now(),
	}})
}
