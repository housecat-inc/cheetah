package api

//go:generate templ generate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/housecat-inc/cheetah/pkg/code"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

const maxRecentLogs = 100

type ServerConfig struct {
	BluePortStart int
	DashboardPort int
	PostgresPort  int
}

type Server struct {
	apps            map[string]*App
	config          ServerConfig
	env             map[string]map[string]string
	lastRegistered  string
	logger          *slog.Logger
	mu              sync.RWMutex
	nextPort1       int
	postgresRunning bool
	postgresURL     string
	startTime       time.Time
	subMu           sync.Mutex
	subscribers     map[chan []byte]struct{}
}

func NewServer(cfg ServerConfig, logger *slog.Logger) *Server {
	return &Server{
		apps:        make(map[string]*App),
		config:      cfg,
		env:         make(map[string]map[string]string),
		logger:      logger,
		nextPort1:   cfg.BluePortStart,
		startTime:   time.Now(),
		subscribers: make(map[chan []byte]struct{}),
	}
}

func (s *Server) SetPostgres(running bool, pgURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.postgresRunning = running
	s.postgresURL = pgURL
}

func (s *Server) Middleware(e *echo.Echo) {
	e.Use(middleware.Recover())
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		HandleError: true,
		LogLatency:  true,
		LogMethod:   true,
		LogStatus:   true,
		LogURI:      true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			if extractSubdomain(c.Request().Host) == "cheetah" {
				return nil
			}
			s.logger.Info("request",
				"method", v.Method,
				"uri", v.URI,
				"status", v.Status,
			)
			return nil
		},
	}))
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			sub := extractSubdomain(c.Request().Host)
			if sub == "" && c.Request().URL.Path == "/auth/callback" {
				return s.handleOAuthBounce(c)
			}
			if sub != "cheetah" {
				return s.handleProxy(c)
			}
			return next(c)
		}
	})
}

func (s *Server) Routes(e *echo.Echo) {
	e.GET("/", s.handleIndex)
	e.GET("/api/events", s.handleEventsStream)
	e.GET("/api/status", s.handleStatus)
	e.GET("/spaces.js", s.handleJS)
	e.GET("/api/apps", s.handleAppList)
	e.POST("/api/apps", s.handleAppPost)
	e.GET("/api/apps/:space", s.handleAppGet)
	e.DELETE("/api/apps/:space", s.handleAppDelete)
	e.POST("/api/apps/:space/logs", s.handleLogPost)
	e.PUT("/api/apps/:space/health", s.handleHealthPut)
	e.GET("/api/env", s.handleEnvList)
	e.POST("/api/env/export", s.handleEnvExport)
	e.POST("/api/env/import", s.handleEnvImport)
	e.GET("/api/env/:app", s.handleEnvGet)
	e.PUT("/api/env/:app", s.handleEnvPut)
	e.DELETE("/api/env/:app/:key", s.handleEnvDelete)
}

// SSE

func (s *Server) broadcast(event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	msg := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, payload))

	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subscribers {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (s *Server) subscribe() chan []byte {
	ch := make(chan []byte, 16)
	s.subMu.Lock()
	s.subscribers[ch] = struct{}{}
	s.subMu.Unlock()
	return ch
}

func (s *Server) unsubscribe(ch chan []byte) {
	s.subMu.Lock()
	delete(s.subscribers, ch)
	s.subMu.Unlock()
	close(ch)
}

// App management

func (s *Server) register(req AppIn) (*App, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, exists := s.apps[req.Space]; exists {
		existing.Config = req.Config
		existing.Dir = req.Dir
		s.lastRegistered = req.Space
		return existing, true
	}

	p1 := s.nextPort1
	p2 := s.nextPort1 + 1
	s.nextPort1 += 2

	app := &App{
		Space:       req.Space,
		Dir:         req.Dir,
		Config:      req.Config,
		DatabaseURL: fmt.Sprintf("postgres://postgres:postgres@localhost:%d/%s?sslmode=disable", s.config.PostgresPort, req.Space),
		Watch:       req.Watch,
		Ports:       Ports{Active: p1, Blue: p1, Green: p2},
		Health:      Health{Status: "unknown"},
		Logs:        make([]Log, 0),
		CreatedAt:   time.Now(),
	}
	s.apps[req.Space] = app
	s.lastRegistered = req.Space
	return app, false
}

func (s *Server) get(space string) (*App, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	app, ok := s.apps[space]
	return app, ok
}

func (s *Server) list() []*App {
	s.mu.RLock()
	defer s.mu.RUnlock()
	apps := make([]*App, 0, len(s.apps))
	for _, app := range s.apps {
		apps = append(apps, app)
	}
	return apps
}

func (s *Server) deregister(space string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[space]; !ok {
		return false
	}
	delete(s.apps, space)
	if s.lastRegistered == space {
		s.lastRegistered = ""
		for name := range s.apps {
			s.lastRegistered = name
			break
		}
	}
	return true
}

func (s *Server) activeTarget() (space string, port int, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.lastRegistered == "" {
		return "", 0, false
	}
	app, exists := s.apps[s.lastRegistered]
	if !exists {
		return "", 0, false
	}

	return app.Space, app.Ports.Active, true
}

func extractSubdomain(host string) string {
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}
	parts := strings.Split(host, ".")
	if len(parts) >= 2 && parts[len(parts)-1] == "localhost" {
		return parts[0]
	}
	return ""
}

func (s *Server) targetForRequest(host string) (space string, port int, ok bool) {
	if sub := extractSubdomain(host); sub != "" && sub != "cheetah" {
		s.mu.RLock()
		app, exists := s.apps[sub]
		s.mu.RUnlock()
		if exists {
			return app.Space, app.Ports.Active, true
		}
	}
	return s.activeTarget()
}

func (s *Server) status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Status{
		AppCount:        len(s.apps),
		PostgresPort:    s.config.PostgresPort,
		PostgresRunning: s.postgresRunning,
		PostgresURL:     s.postgresURL,
		Uptime:          time.Since(s.startTime).Truncate(time.Second).String(),
	}
}

func (s *Server) appendLogs(space string, entries []Log) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.apps[space]
	if !ok {
		return false
	}
	app.Logs = append(app.Logs, entries...)
	if len(app.Logs) > maxRecentLogs {
		app.Logs = app.Logs[len(app.Logs)-maxRecentLogs:]
	}
	return true
}

func (s *Server) updateHealth(space, status string, portActive int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.apps[space]
	if !ok {
		return false
	}
	app.Health = Health{Status: status, UpdatedAt: time.Now()}
	if portActive > 0 {
		app.Ports.Active = portActive
	}
	return true
}

// HTTP handlers

func (s *Server) handleStatus(c echo.Context) error {
	return c.JSON(http.StatusOK, s.status())
}

func (s *Server) handleAppList(c echo.Context) error {
	return c.JSON(http.StatusOK, s.list())
}

func (s *Server) handleAppPost(c echo.Context) error {
	var req AppIn
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if req.Space == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "space is required"})
	}

	app, existed := s.register(req)

	s.logger.Info("register", "space", app.Space, "existed", existed)
	s.broadcast("app", app)

	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}

	appName := code.AppName(req.Dir, req.Space)
	return c.JSON(status, AppOut{
		DatabaseURL: app.DatabaseURL,
		Env:         s.envGet(appName),
		Ports:       app.Ports,
		Space:       app.Space,
	})
}

func (s *Server) handleAppGet(c echo.Context) error {
	app, ok := s.get(c.Param("space"))
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	return c.JSON(http.StatusOK, app)
}

func (s *Server) handleAppDelete(c echo.Context) error {
	space := c.Param("space")
	if !s.deregister(space) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	s.logger.Info("deregister", "space", space)
	s.broadcast("deregister", map[string]string{"space": space})
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleLogPost(c echo.Context) error {
	space := c.Param("space")
	var entries []Log
	if err := c.Bind(&entries); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if !s.appendLogs(space, entries) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleHealthPut(c echo.Context) error {
	space := c.Param("space")
	var body struct {
		PortActive int    `json:"port_active"`
		Status     string `json:"status"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if !s.updateHealth(space, body.Status, body.PortActive) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}

	if app, ok := s.get(space); ok {
		s.broadcast("app", app)
	}

	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleEventsStream(c echo.Context) error {
	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)

	apps := s.list()
	payload, _ := json.Marshal(apps)
	fmt.Fprintf(w, "event: init\ndata: %s\n\n", payload)
	w.Flush()

	ch := s.subscribe()
	defer s.unsubscribe(ch)

	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			w.Write(msg)
			w.Flush()
		}
	}
}

// Env management

func (s *Server) envGet(app string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vars := s.env[app]
	if vars == nil {
		return nil
	}
	out := make(map[string]string, len(vars))
	for k, v := range vars {
		out[k] = v
	}
	return out
}

func (s *Server) envReplace(app string, vars map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(vars) == 0 {
		delete(s.env, app)
		return
	}
	s.env[app] = make(map[string]string, len(vars))
	for k, v := range vars {
		s.env[app][k] = v
	}
}

func (s *Server) envDeleteKey(app, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	vars := s.env[app]
	if vars == nil {
		return false
	}
	delete(vars, key)
	if len(vars) == 0 {
		delete(s.env, app)
	}
	return true
}

func (s *Server) envList() map[string]map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]map[string]string, len(s.env))
	for app, vars := range s.env {
		cp := make(map[string]string, len(vars))
		for k, v := range vars {
			cp[k] = v
		}
		out[app] = cp
	}
	return out
}

func (s *Server) handleEnvList(c echo.Context) error {
	return c.JSON(http.StatusOK, s.envList())
}

func (s *Server) handleEnvGet(c echo.Context) error {
	vars := s.envGet(c.Param("app"))
	if vars == nil {
		return c.JSON(http.StatusOK, map[string]string{})
	}
	return c.JSON(http.StatusOK, vars)
}

func (s *Server) handleEnvPut(c echo.Context) error {
	app := c.Param("app")
	var vars map[string]string
	if err := json.NewDecoder(c.Request().Body).Decode(&vars); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	s.envReplace(app, vars)
	s.broadcast("env", map[string]any{"app": app, "vars": s.envGet(app)})
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleEnvDelete(c echo.Context) error {
	app := c.Param("app")
	key := c.Param("key")
	if !s.envDeleteKey(app, key) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	s.broadcast("env", map[string]any{"app": app, "vars": s.envGet(app)})
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleEnvExport(c echo.Context) error {
	var in EnvExportIn
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if in.App == "" || in.Passphrase == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "app and passphrase required"})
	}

	vars := s.envGet(in.App)
	if vars == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "no env for app"})
	}

	blob, err := encryptEnv(in.App, vars, in.Passphrase)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, EnvExportOut{Blob: blob})
}

func (s *Server) handleEnvImport(c echo.Context) error {
	var in EnvImportIn
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if in.Blob == "" || in.Passphrase == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "blob and passphrase required"})
	}

	app, vars, err := decryptEnv(in.Blob, in.Passphrase)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	s.envReplace(app, vars)
	s.broadcast("env", map[string]any{"app": app, "vars": s.envGet(app)})

	return c.JSON(http.StatusOK, EnvImportOut{App: app, Vars: vars})
}

// OAuth bouncer

func (s *Server) handleOAuthBounce(c echo.Context) error {
	state := c.QueryParam("state")
	space, appState, ok := strings.Cut(state, "|")

	q := url.Values{}
	for k, vs := range c.QueryParams() {
		for _, v := range vs {
			if k == "state" {
				continue
			}
			q.Add(k, v)
		}
	}

	if ok && space != "" {
		q.Set("state", appState)
		target := fmt.Sprintf("http://%s.localhost:%d/auth/callback?%s", space, s.config.DashboardPort, q.Encode())
		return c.Redirect(http.StatusTemporaryRedirect, target)
	}

	q.Set("state", state)
	target := fmt.Sprintf("http://localhost:%d/auth/callback?%s", s.config.DashboardPort, q.Encode())
	return c.Redirect(http.StatusTemporaryRedirect, target)
}

// Reverse proxy

func (s *Server) handleProxy(c echo.Context) error {
	space, port, ok := s.targetForRequest(c.Request().Host)
	if !ok {
		return c.Redirect(http.StatusTemporaryRedirect, fmt.Sprintf("http://cheetah.localhost:%d/", s.config.DashboardPort))
	}

	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			ct := resp.Header.Get("Content-Type")
			if !strings.Contains(ct, "text/html") {
				return nil
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return err
			}
			injected := strings.Replace(
				string(body),
				"</body>",
				fmt.Sprintf(`<script src="//cheetah.localhost:%d/spaces.js" data-space="%s" data-port="%d"></script>`+"\n</body>", s.config.DashboardPort, space, port),
				1,
			)
			resp.Body = io.NopCloser(bytes.NewReader([]byte(injected)))
			resp.ContentLength = int64(len(injected))
			resp.Header.Set("Content-Length", strconv.Itoa(len(injected)))
			return nil
		},
	}

	proxy.ServeHTTP(c.Response(), c.Request())
	return nil
}

// Status bubble JS

const spacesJS = `(function() {
  const script = document.currentScript;
  const space = script?.getAttribute("data-space") || "";
  const initialPort = script?.getAttribute("data-port") || "";

  const el = document.createElement("div");
  el.id = "__cheetah";
  el.innerHTML = '<span class="__sc-dot"></span> <span class="__sc-label"></span>';
  document.body.appendChild(el);

  const menu = document.createElement("div");
  menu.id = "__cheetah-menu";
  document.body.appendChild(menu);

  const style = document.createElement("style");
  style.textContent = ` + "`" + `
    #__cheetah {
      position: fixed; bottom: 12px; right: 12px; z-index: 2147483647;
      background: #1a1a2e; color: #e0e0e0; border: 1px solid #2a2a3e;
      border-radius: 20px; padding: 6px 14px; font: 12px/1 system-ui, sans-serif;
      cursor: pointer; display: flex; align-items: center; gap: 6px;
      box-shadow: 0 2px 8px rgba(0,0,0,0.4); transition: opacity 0.2s;
      opacity: 0.85; user-select: none;
    }
    #__cheetah:hover { opacity: 1; }
    .__sc-dot {
      width: 8px; height: 8px; border-radius: 50%;
      background: #888; display: inline-block;
    }
    .__sc-dot.healthy { background: #4ade80; }
    .__sc-dot.unhealthy { background: #ef4444; }
    .__sc-dot.unknown { background: #888; }
    .__sc-dot.building { background: #facc15; }
    #__cheetah-menu {
      position: fixed; bottom: 44px; right: 12px; z-index: 2147483647;
      background: #1a1a2e; color: #e0e0e0; border: 1px solid #2a2a3e;
      border-radius: 8px; padding: 4px 0; font: 12px/1 system-ui, sans-serif;
      box-shadow: 0 2px 12px rgba(0,0,0,0.5);
      display: none; min-width: 180px;
    }
    #__cheetah-menu.open { display: block; }
    .__sc-item {
      display: flex; align-items: center; gap: 8px;
      padding: 8px 14px; cursor: pointer; text-decoration: none; color: #e0e0e0;
    }
    .__sc-item:hover { background: #2a2a3e; }
    .__sc-item.active { background: #2a2a3e; font-weight: 600; }
    .__sc-item .info { color: #888; font-size: 11px; margin-left: auto; }
    .__sc-sep { border-top: 1px solid #2a2a3e; margin: 4px 0; }
  ` + "`" + `;
  document.head.appendChild(style);

  const dot = el.querySelector(".__sc-dot");
  const label = el.querySelector(".__sc-label");
  label.textContent = space + " :" + initialPort;

  if (space === "cheetah") dot.className = "__sc-dot healthy";

  let allApps = {};
  let menuOpen = false;

  el.addEventListener("click", function(e) {
    e.stopPropagation();
    menuOpen = !menuOpen;
    renderMenu();
  });

  document.addEventListener("click", function() {
    if (menuOpen) { menuOpen = false; renderMenu(); }
  });

  menu.addEventListener("click", function(e) { e.stopPropagation(); });

  function renderMenu() {
    if (!menuOpen) { menu.className = ""; menu.id = "__cheetah-menu"; return; }
    menu.className = "open"; menu.id = "__cheetah-menu";
    const list = Object.values(allApps);
    let h = "";
    for (const a of list) {
      const p = a.ports.active;
      const active = a.space === space ? " active" : "";
      const href = location.protocol + "//" + a.space + ".localhost:" + location.port + "/";
      h += '<a class="__sc-item' + active + '" href="' + href + '">' +
        '<span class="__sc-dot ' + a.health.status + '"></span>' +
        a.space +
        '<span class="info">:' + p + '</span></a>';
    }
    if (list.length > 0) h += '<div class="__sc-sep"></div>';
    const scActive = space === "cheetah" ? " active" : "";
    h += '<a class="__sc-item' + scActive + '" href="//cheetah.localhost:' + location.port + '/">' +
      '<span class="__sc-dot healthy"></span>cheetah' +
      '<span class="info">:' + location.port + '</span></a>';
    menu.innerHTML = h;
  }

  let lastPort = initialPort;
  let reloading = false;

  function update(app) {
    if (!app) return;
    allApps[app.space] = app;
    if (menuOpen) renderMenu();

    if (app.space !== space) return;

    dot.className = "__sc-dot " + app.health.status;
    const p = app.ports.active;
    label.textContent = app.space + " :" + p;

    if (String(p) !== lastPort && app.health.status === "healthy" && !reloading) {
      reloading = true;
      label.textContent = "reloading...";
      setTimeout(() => location.reload(), 300);
    }
    lastPort = String(p);
  }

  const es = new EventSource("//cheetah.localhost:" + location.port + "/api/events");

  es.addEventListener("init", function(e) {
    const apps = JSON.parse(e.data);
    allApps = {};
    for (const a of apps) allApps[a.space] = a;
    const app = allApps[space];
    if (app) update(app);
  });

  es.addEventListener("app", function(e) {
    update(JSON.parse(e.data));
  });

  es.addEventListener("deregister", function(e) {
    const data = JSON.parse(e.data);
    delete allApps[data.space];
    if (menuOpen) renderMenu();
    if (data.space === space) {
      dot.className = "__sc-dot unhealthy";
      label.textContent = space + " disconnected";
    }
  });

  es.onerror = function() {
    dot.className = "__sc-dot unhealthy";
  };
})();
`

func (s *Server) handleJS(c echo.Context) error {
	return c.Blob(http.StatusOK, "application/javascript", []byte(spacesJS))
}

func (s *Server) handleIndex(c echo.Context) error {
	return Dashboard(s.config.DashboardPort, s.status()).Render(c.Request().Context(), c.Response().Writer)
}

// State persistence

type serverState struct {
	Apps           map[string]*App              `json:"apps"`
	Env            map[string]map[string]string `json:"env,omitempty"`
	LastRegistered string                       `json:"last_registered"`
	NextPort1      int                          `json:"next_port1"`
}

func (s *Server) SaveState(path string) {
	s.mu.RLock()
	state := serverState{
		Apps:           s.apps,
		Env:            s.env,
		LastRegistered: s.lastRegistered,
		NextPort1:      s.nextPort1,
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		s.logger.Warn("failed to marshal state", "error", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		s.logger.Warn("failed to write state", "error", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		s.logger.Warn("failed to rename state file", "error", err)
	}
}

func (s *Server) LoadState(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var state serverState
	if err := json.Unmarshal(data, &state); err != nil {
		s.logger.Warn("failed to parse state file, starting fresh", "error", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.apps = state.Apps
	s.env = state.Env
	s.nextPort1 = state.NextPort1
	if s.nextPort1 < s.config.BluePortStart {
		s.nextPort1 = s.config.BluePortStart
	}
	s.lastRegistered = ""

	if s.apps == nil {
		s.apps = make(map[string]*App)
	}
	if s.env == nil {
		s.env = make(map[string]map[string]string)
	}
	for _, app := range s.apps {
		app.Health.Status = "unknown"
		if app.Ports.Blue < s.config.BluePortStart {
			app.Ports.Blue = s.nextPort1
			app.Ports.Green = s.nextPort1 + 1
			s.nextPort1 += 2
		}
	}

	s.logger.Info("state", "apps", len(s.apps))

	go s.probeHealth()
}

func (s *Server) probeHealth() {
	client := &http.Client{Timeout: 1 * time.Second}

	s.mu.RLock()
	apps := make([]*App, 0, len(s.apps))
	for _, app := range s.apps {
		apps = append(apps, app)
	}
	s.mu.RUnlock()

	for _, app := range apps {
		port := app.Ports.Active
		resp, err := client.Get(fmt.Sprintf("http://localhost:%d/health", port))
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			s.mu.Lock()
			app.Health = Health{Status: "healthy", UpdatedAt: time.Now()}
			if s.lastRegistered == "" {
				s.lastRegistered = app.Space
			}
			s.mu.Unlock()
			s.logger.Info("probe", "space", app.Space, "status", "healthy", "port", port)
			s.broadcast("app", app)
		}
	}
}

func (s *Server) PeriodicSave(path string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		s.SaveState(path)
	}
}
