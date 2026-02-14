package main

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
		LogStatus: true,
		LogURI:    true,
		LogMethod: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			logger.Info("request",
				"method", v.Method,
				"uri", v.URI,
				"status", v.Status,
			)
			return nil
		},
	}))

	// API routes
	a := e.Group("/api")
	a.GET("/status", reg.handleStatus)
	a.GET("/apps", reg.handleListApps)
	a.POST("/apps", reg.handleRegisterApp)
	a.GET("/apps/:space", reg.handleGetApp)
	a.DELETE("/apps/:space", reg.handleDeregisterApp)
	a.POST("/apps/:space/logs", reg.handleAppendLogs)
	a.PUT("/apps/:space/health", reg.handleUpdateHealth)

	// Dashboard
	e.GET("/", reg.handleDashboard)

	go func() {
		addr := fmt.Sprintf(":%d", dashboardPort)
		logger.Info("starting dashboard", "addr", addr)
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
	nextProxyPort   int
	nextBluePort    int
	postgresRunning bool
	postgresURL     string
	startTime       time.Time
	logger          *slog.Logger
}

func newRegistry(logger *slog.Logger) *registry {
	return &registry{
		apps:          make(map[string]*api.App),
		nextProxyPort: proxyPortStart,
		nextBluePort:  bluePortStart,
		startTime:     time.Now(),
		logger:        logger,
	}
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
	return true
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

func (r *registry) updateHealth(space, status string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	app, ok := r.apps[space]
	if !ok {
		return false
	}
	app.HealthStatus = status
	app.LastHealthCheck = time.Now()
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
		Status string `json:"status"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if !r.updateHealth(space, body.Status) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	return c.NoContent(http.StatusNoContent)
}

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
