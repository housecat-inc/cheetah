package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/housecat-inc/spacecat/pkg/api"
)

const (
	dashboardPort  = 8080
	postgresPort   = 54320
	proxyPortStart = 3000
	bluePortStart  = 4000
	maxRecentLogs  = 100
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	reg := newRegistry(logger)

	// Start embedded postgres
	pg := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Port(postgresPort).
			Logger(os.Stderr),
	)
	if err := pg.Start(); err != nil {
		logger.Error("failed to start embedded postgres", "error", err)
		os.Exit(1)
	}
	reg.mu.Lock()
	reg.postgresRunning = true
	reg.postgresURL = fmt.Sprintf("postgres://localhost:%d/postgres?sslmode=disable", postgresPort)
	reg.mu.Unlock()
	logger.Info("embedded postgres started", "port", postgresPort)

	// Start Echo
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover())
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogStatus:   true,
		LogURI:      true,
		LogMethod:   true,
		LogLatency:  true,
		HandleError: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			if strings.HasPrefix(v.URI, "/_spaces/api/events") {
				return nil // don't log SSE connections
			}
			logger.Info("request",
				"method", v.Method,
				"uri", v.URI,
				"status", v.Status,
			)
			return nil
		},
	}))

	// Dashboard and API under /_spaces/
	s := e.Group("/_spaces")
	s.GET("/", reg.handleDashboard)
	s.GET("/api/status", reg.handleStatus)
	s.GET("/api/apps", reg.handleListApps)
	s.POST("/api/apps", reg.handleRegisterApp)
	s.GET("/api/apps/:space", reg.handleGetApp)
	s.DELETE("/api/apps/:space", reg.handleDeregisterApp)
	s.POST("/api/apps/:space/logs", reg.handleAppendLogs)
	s.PUT("/api/apps/:space/health", reg.handleUpdateHealth)
	s.GET("/api/events", reg.handleSSE)

	// Status bubble JS
	e.GET("/_spaces.js", reg.handleSpacesJS)

	// Reverse proxy catch-all — must be last
	e.Any("/*", reg.handleProxy)
	e.Any("/", reg.handleProxy)

	go func() {
		addr := fmt.Sprintf(":%d", dashboardPort)
		logger.Info("starting spacecat", "addr", addr)
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.Shutdown(ctx); err != nil {
		logger.Error("server shutdown error", "error", err)
	}
	if err := pg.Stop(); err != nil {
		logger.Error("postgres shutdown error", "error", err)
	}
	logger.Info("shutdown complete")
}

// registry holds all in-memory state for the dashboard.
type registry struct {
	mu              sync.RWMutex
	apps            map[string]*api.App
	lastRegistered  string
	nextProxyPort   int
	nextBluePort    int
	postgresRunning bool
	postgresURL     string
	startTime       time.Time
	logger          *slog.Logger

	// SSE subscribers
	subMu       sync.Mutex
	subscribers map[chan []byte]struct{}
}

func newRegistry(logger *slog.Logger) *registry {
	return &registry{
		apps:          make(map[string]*api.App),
		nextProxyPort: proxyPortStart,
		nextBluePort:  bluePortStart,
		startTime:     time.Now(),
		logger:        logger,
		subscribers:   make(map[chan []byte]struct{}),
	}
}

// broadcast sends an SSE event to all connected clients.
func (r *registry) broadcast(event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	msg := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, payload))

	r.subMu.Lock()
	defer r.subMu.Unlock()
	for ch := range r.subscribers {
		select {
		case ch <- msg:
		default: // drop if subscriber is slow
		}
	}
}

func (r *registry) subscribe() chan []byte {
	ch := make(chan []byte, 16)
	r.subMu.Lock()
	r.subscribers[ch] = struct{}{}
	r.subMu.Unlock()
	return ch
}

func (r *registry) unsubscribe(ch chan []byte) {
	r.subMu.Lock()
	delete(r.subscribers, ch)
	r.subMu.Unlock()
	close(ch)
}

func (r *registry) register(req api.RegisterRequest) (*api.App, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.apps[req.Space]; exists {
		return nil, fmt.Errorf("space %q already registered", req.Space)
	}

	proxy := r.nextProxyPort
	blue := r.nextBluePort
	green := r.nextBluePort + 1
	r.nextProxyPort++
	r.nextBluePort += 2

	app := &api.App{
		Space:          req.Space,
		ConfigFile:     req.ConfigFile,
		TemplateDBURL:  fmt.Sprintf("postgres://localhost:%d/t_%s?sslmode=disable", postgresPort, req.Space),
		DatabaseURL:    fmt.Sprintf("postgres://localhost:%d/%s?sslmode=disable", postgresPort, req.Space),
		WatchPatterns:  req.WatchPatterns,
		IgnorePatterns: req.IgnorePatterns,
		ProxyPort:      proxy,
		BluePort:       blue,
		GreenPort:      green,
		ActiveColor:    "blue",
		HealthStatus:   "unknown",
		RecentLogs:     make([]api.LogEntry, 0),
		RegisteredAt:   time.Now(),
	}
	r.apps[req.Space] = app
	r.lastRegistered = req.Space
	return app, nil
}

func (r *registry) get(space string) (*api.App, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	app, ok := r.apps[space]
	return app, ok
}

func (r *registry) list() []*api.App {
	r.mu.RLock()
	defer r.mu.RUnlock()
	apps := make([]*api.App, 0, len(r.apps))
	for _, app := range r.apps {
		apps = append(apps, app)
	}
	return apps
}

func (r *registry) deregister(space string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.apps[space]; !ok {
		return false
	}
	delete(r.apps, space)
	if r.lastRegistered == space {
		r.lastRegistered = ""
		for name := range r.apps {
			r.lastRegistered = name
			break
		}
	}
	return true
}

// activeTarget returns the most recently registered app's active port.
func (r *registry) activeTarget() (space string, port int, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.lastRegistered == "" {
		return "", 0, false
	}
	app, exists := r.apps[r.lastRegistered]
	if !exists {
		return "", 0, false
	}

	if app.ActiveColor == "green" {
		return app.Space, app.GreenPort, true
	}
	return app.Space, app.BluePort, true
}

func (r *registry) status() api.Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return api.Status{
		PostgresRunning: r.postgresRunning,
		PostgresPort:    postgresPort,
		PostgresURL:     r.postgresURL,
		Uptime:          time.Since(r.startTime).Truncate(time.Second).String(),
		AppCount:        len(r.apps),
	}
}

func (r *registry) appendLogs(space string, entries []api.LogEntry) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	app, ok := r.apps[space]
	if !ok {
		return false
	}
	app.RecentLogs = append(app.RecentLogs, entries...)
	if len(app.RecentLogs) > maxRecentLogs {
		app.RecentLogs = app.RecentLogs[len(app.RecentLogs)-maxRecentLogs:]
	}
	return true
}

func (r *registry) updateHealth(space, status, activeColor string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	app, ok := r.apps[space]
	if !ok {
		return false
	}
	app.HealthStatus = status
	app.LastHealthCheck = time.Now()
	if activeColor == "blue" || activeColor == "green" {
		app.ActiveColor = activeColor
	}
	return true
}

// HTTP handlers

func (r *registry) handleStatus(c echo.Context) error {
	return c.JSON(http.StatusOK, r.status())
}

func (r *registry) handleListApps(c echo.Context) error {
	return c.JSON(http.StatusOK, r.list())
}

func (r *registry) handleRegisterApp(c echo.Context) error {
	var req api.RegisterRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if req.Space == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "space is required"})
	}

	app, err := r.register(req)
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}

	r.logger.Info("app registered", "space", app.Space, "proxy", app.ProxyPort)
	r.broadcast("app", app)

	return c.JSON(http.StatusCreated, api.RegisterResponse{
		Space:         app.Space,
		ProxyPort:     app.ProxyPort,
		BluePort:      app.BluePort,
		GreenPort:     app.GreenPort,
		TemplateDBURL: app.TemplateDBURL,
		DatabaseURL:   app.DatabaseURL,
	})
}

func (r *registry) handleGetApp(c echo.Context) error {
	app, ok := r.get(c.Param("space"))
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	return c.JSON(http.StatusOK, app)
}

func (r *registry) handleDeregisterApp(c echo.Context) error {
	space := c.Param("space")
	if !r.deregister(space) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	r.logger.Info("app deregistered", "space", space)
	r.broadcast("deregister", map[string]string{"space": space})
	return c.NoContent(http.StatusNoContent)
}

func (r *registry) handleAppendLogs(c echo.Context) error {
	space := c.Param("space")
	var entries []api.LogEntry
	if err := c.Bind(&entries); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if !r.appendLogs(space, entries) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	return c.NoContent(http.StatusNoContent)
}

func (r *registry) handleUpdateHealth(c echo.Context) error {
	space := c.Param("space")
	var body struct {
		Status      string `json:"status"`
		ActiveColor string `json:"active_color"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if !r.updateHealth(space, body.Status, body.ActiveColor) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}

	// Broadcast updated app state to SSE clients
	if app, ok := r.get(space); ok {
		r.broadcast("app", app)
	}

	return c.NoContent(http.StatusNoContent)
}

// SSE handler

func (r *registry) handleSSE(c echo.Context) error {
	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Send current state immediately
	apps := r.list()
	payload, _ := json.Marshal(apps)
	fmt.Fprintf(w, "event: init\ndata: %s\n\n", payload)
	w.Flush()

	ch := r.subscribe()
	defer r.unsubscribe(ch)

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

// Reverse proxy handler

func (r *registry) handleProxy(c echo.Context) error {
	space, port, ok := r.activeTarget()
	if !ok {
		return c.Redirect(http.StatusTemporaryRedirect, "/_spaces/")
	}

	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
		},
		FlushInterval: -1, // stream SSE immediately
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
				fmt.Sprintf(`<script src="/_spaces.js" data-space="%s" data-port="%d"></script>`+"\n</body>", space, port),
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

// Status bubble JS — uses SSE instead of polling

const spacesJS = `(function() {
  const script = document.currentScript;
  const space = script?.getAttribute("data-space") || "";
  const initialPort = script?.getAttribute("data-port") || "";

  const el = document.createElement("div");
  el.id = "__spacecat";
  el.innerHTML = '<span class="__sc-dot"></span> <span class="__sc-label"></span>';
  document.body.appendChild(el);

  const style = document.createElement("style");
  style.textContent = ` + "`" + `
    #__spacecat {
      position: fixed; bottom: 12px; right: 12px; z-index: 2147483647;
      background: #1a1a2e; color: #e0e0e0; border: 1px solid #2a2a3e;
      border-radius: 20px; padding: 6px 14px; font: 12px/1 system-ui, sans-serif;
      cursor: pointer; display: flex; align-items: center; gap: 6px;
      box-shadow: 0 2px 8px rgba(0,0,0,0.4); transition: opacity 0.2s;
      opacity: 0.85; user-select: none;
    }
    #__spacecat:hover { opacity: 1; }
    .__sc-dot {
      width: 8px; height: 8px; border-radius: 50%;
      background: #888; display: inline-block;
    }
    .__sc-dot.healthy { background: #4ade80; }
    .__sc-dot.unhealthy { background: #ef4444; }
    .__sc-dot.unknown { background: #888; }
    .__sc-dot.building { background: #facc15; }
  ` + "`" + `;
  document.head.appendChild(style);

  const dot = el.querySelector(".__sc-dot");
  const label = el.querySelector(".__sc-label");
  label.textContent = space + " :" + initialPort;

  el.addEventListener("click", function() {
    window.open("/_spaces/", "_blank");
  });

  let lastPort = initialPort;
  let reloading = false;

  function update(app) {
    if (!app || app.space !== space) return;

    dot.className = "__sc-dot " + app.health_status;
    const p = app.active_color === "green" ? app.green_port : app.blue_port;
    label.textContent = app.space + " :" + p;

    // Auto-reload when proxy switches to a new healthy port
    if (String(p) !== lastPort && app.health_status === "healthy" && !reloading) {
      reloading = true;
      label.textContent = "reloading...";
      setTimeout(() => location.reload(), 300);
    }
    lastPort = String(p);
  }

  const es = new EventSource("/_spaces/api/events");

  es.addEventListener("init", function(e) {
    const apps = JSON.parse(e.data);
    const app = apps.find(a => a.space === space);
    if (app) update(app);
  });

  es.addEventListener("app", function(e) {
    update(JSON.parse(e.data));
  });

  es.addEventListener("deregister", function(e) {
    const data = JSON.parse(e.data);
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

func (r *registry) handleSpacesJS(c echo.Context) error {
	return c.Blob(http.StatusOK, "application/javascript", []byte(spacesJS))
}

// Dashboard

var dashboardTmpl = template.Must(template.New("dashboard").Parse(`<!DOCTYPE html>
<html>
<head>
<title>Spacecat Dashboard</title>
<meta charset="utf-8">
<style>
  body { font-family: system-ui, sans-serif; margin: 2rem; background: #0a0a0a; color: #e0e0e0; }
  h1 { color: #f0f0f0; }
  .status { background: #1a1a2e; padding: 1rem; border-radius: 8px; margin-bottom: 2rem; }
  .status span { margin-right: 2rem; }
  .dot { display: inline-block; width: 10px; height: 10px; border-radius: 50%; margin-right: 4px; }
  .dot.on { background: #4ade80; }
  .dot.off { background: #ef4444; }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: 0.5rem 1rem; border-bottom: 1px solid #2a2a3e; }
  th { color: #888; font-weight: 500; font-size: 0.85rem; text-transform: uppercase; }
  tr:hover { background: #1a1a2e; }
  .healthy { color: #4ade80; }
  .unhealthy { color: #ef4444; }
  .unknown { color: #888; }
  code { background: #1a1a2e; padding: 2px 6px; border-radius: 4px; font-size: 0.85rem; }
  .empty { text-align: center; padding: 3rem; color: #666; }
</style>
</head>
<body>
<h1>Spacecat</h1>
<div class="status">
  <span><span class="dot {{if .Status.PostgresRunning}}on{{else}}off{{end}}"></span> Postgres :{{.Status.PostgresPort}}</span>
  <span>Uptime: {{.Status.Uptime}}</span>
  <span>Apps: {{.Status.AppCount}}</span>
</div>
{{if .Apps}}
<table>
<thead>
<tr>
  <th>Space</th>
  <th>Config</th>
  <th>Template DB</th>
  <th>Proxy</th>
  <th>Blue</th>
  <th>Green</th>
  <th>Active</th>
  <th>Health</th>
  <th>Watch</th>
  <th>Logs</th>
</tr>
</thead>
<tbody>
{{range .Apps}}
<tr>
  <td><strong>{{.Space}}</strong></td>
  <td><code>{{.ConfigFile}}</code></td>
  <td><code>{{.TemplateDBURL}}</code></td>
  <td>:{{.ProxyPort}}</td>
  <td>:{{.BluePort}}</td>
  <td>:{{.GreenPort}}</td>
  <td>{{.ActiveColor}}</td>
  <td class="{{.HealthStatus}}">{{.HealthStatus}}</td>
  <td>{{range .WatchPatterns}}<code>{{.}}</code> {{end}}</td>
  <td>{{len .RecentLogs}}</td>
</tr>
{{end}}
</tbody>
</table>
{{else}}
<div class="empty">No apps registered. Run <code>go run main.go</code> in a child app to register.</div>
{{end}}
</body>
</html>`))

func (r *registry) handleDashboard(c echo.Context) error {
	data := struct {
		Status api.Status
		Apps   []*api.App
	}{
		Status: r.status(),
		Apps:   r.list(),
	}
	return dashboardTmpl.Execute(c.Response().Writer, data)
}
