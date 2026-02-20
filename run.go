package cheetah

import (
	"bufio"
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

const defaultURL = "http://localhost:50000"

func Run(defaults ...map[string]string) {
	url := config.EnvOr("CHEETAH_URL", defaultURL)
	space, err := code.System()
	if err != nil {
		slog.Error("failed to determine space", "error", err)
		os.Exit(1)
	}

	var defs map[string]string
	if len(defaults) > 0 {
		defs = defaults[0]
	}

	cfg := config.Load(config.DefaultEnv(), space.Dir, config.LoadIn{Defaults: defs})

	l := logs.New(space.Name)
	slog.SetDefault(l)

	if err := ensureInfra(url); err != nil {
		l.Error("failed to start infrastructure", "error", err)
		os.Exit(1)
	}

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

	cfg = config.Load(config.DefaultEnv(), space.Dir, config.LoadIn{Defaults: defs, ProxyEnv: resp.Env})

	if len(resp.Env) > 0 {
		client.AppPost(api.AppIn{
			Config: cfg.Providers,
			Dir:    space.Dir,
			Space:  space.Name,
			Watch:  api.Watch{Match: []string{".envrc", "*.go", "*.sql", "*.templ", "go.mod"}},
		})
	}

	ports := port.New(resp.Ports.Blue, resp.Ports.Green, port.DefaultConfig(client, space.Name))

	runner := &appRunner{
		appEnv:      cfg.Env,
		appName:     code.AppName(space.Dir, space.Name),
		client:      client,
		cmds:        make(map[int]*exec.Cmd),
		defs:        defs,
		dir:         space.Dir,
		logger:      l,
		ports:       ports,
		proxyEnv:    resp.Env,
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
		runner.sendLog("error", fmt.Sprintf("initial build failed: %v", err))
	} else {
		ports.ReportHealth("unknown")
		ports.WaitForHealthy(resp.Ports.Blue)
		ports.ReportHealth("healthy")
	}

	w := watch.New(space.Dir, []string{".envrc", "*.go", "*.sql", "*.templ", "go.mod"}, nil, func(path string) {
		runner.rebuild(path)
	})
	w.Start()

	go runner.watchEnvEvents()

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
	appName     string
	client      *api.Client
	cmds        map[int]*exec.Cmd
	defs        map[string]string
	dir         string
	logger      *slog.Logger
	mu          sync.Mutex
	ports       *port.Manager
	proxyEnv    map[string]string
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
		cfg := config.Load(config.DefaultEnv(), r.dir, config.LoadIn{Defaults: r.defs, ProxyEnv: r.proxyEnv})
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

func (r *appRunner) watchEnvEvents() {
	for {
		r.listenEnvEvents()
		time.Sleep(2 * time.Second)
	}
}

func (r *appRunner) listenEnvEvents() {
	resp, err := http.Get(r.cheetahURL + "/api/events")
	if err != nil {
		return
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	var eventType string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") && eventType == "env" {
			data := strings.TrimPrefix(line, "data: ")
			var payload struct {
				App  string            `json:"app"`
				Vars map[string]string `json:"vars"`
			}
			if json.Unmarshal([]byte(data), &payload) == nil && payload.App == r.appName {
				r.envReload(payload.Vars)
			}
			eventType = ""
		} else if line == "" {
			eventType = ""
		}
	}
}

func (r *appRunner) envReload(vars map[string]string) {
	r.logger.Info("env update from dashboard")
	r.proxyEnv = vars
	cfg := config.Load(config.DefaultEnv(), r.dir, config.LoadIn{Defaults: r.defs, ProxyEnv: r.proxyEnv})
	r.appEnv = cfg.Env

	r.client.AppPost(api.AppIn{
		Config: cfg.Providers,
		Dir:    r.dir,
		Space:  r.space,
		Watch:  api.Watch{Match: []string{".envrc", "*.go", "*.sql", "*.templ", "go.mod"}},
	})

	if !r.ports.Swap(r.start, r.stopPort) {
		r.logger.Error("swap failed")
		r.sendLog("error", "swap failed after env update")
	}
}

func ensureInfra(url string) error {
	c := &http.Client{Timeout: 1 * time.Second}
	latest := latestVersion()
	status, err := checkStatus(c, url)
	running := err == nil

	if running && (latest == "" || latest == status.Version || status.Version == "dev") {
		return nil
	}

	if running {
		slog.Info("updating cheetah", "from", status.Version, "to", latest)
	} else {
		slog.Info("starting cheetah")
	}

	ref := "@latest"
	if latest != "" {
		ref = "@" + latest
	}
	install := exec.Command("go", "install", "github.com/housecat-inc/cheetah/cmd/cheetah"+ref)
	install.Env = append(os.Environ(), "GOPROXY=direct")
	install.Stdout = os.Stdout
	install.Stderr = os.Stderr
	if err := install.Run(); err != nil {
		return errors.Wrap(err, "go install cheetah")
	}

	if running {
		exec.Command("cheetah", "stop").Run()
		time.Sleep(time.Second)
	}

	return startCheetah(c, url)
}

func startCheetah(c *http.Client, url string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return errors.Wrap(err, "user home dir")
	}
	dir := filepath.Join(home, ".cheetah")
	os.MkdirAll(dir, 0o755)

	logFile, err := os.OpenFile(filepath.Join(dir, "cheetah.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return errors.Wrap(err, "open cheetah log")
	}

	cmd := exec.Command("cheetah")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return errors.Wrap(err, "start cheetah")
	}
	logFile.Close()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	for i := 0; i < 60; i++ {
		time.Sleep(500 * time.Millisecond)
		select {
		case err := <-exited:
			return errors.Wrap(err, "cheetah process exited")
		default:
		}
		if _, err := checkStatus(c, url); err == nil {
			slog.Info("cheetah ready")
			return nil
		}
	}

	return errors.New("cheetah did not become ready")
}

func checkStatus(c *http.Client, url string) (api.Status, error) {
	resp, err := c.Get(url + "/api/status")
	if err != nil {
		return api.Status{}, err
	}
	defer resp.Body.Close()
	var status api.Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return api.Status{}, err
	}
	return status, nil
}

func latestVersion() string {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "github.com/housecat-inc/cheetah@latest").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
