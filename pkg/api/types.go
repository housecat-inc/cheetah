package api

import "time"

type App struct {
	Config          []string   `json:"config"`
	DatabaseURL     string     `json:"database_url"`
	Dir             string     `json:"dir"`
	HealthStatus    string     `json:"health_status"`
	IgnorePatterns  []string   `json:"ignore_patterns"`
	LastHealthCheck time.Time  `json:"last_health_check"`
	Port1           int        `json:"port1"`
	Port2           int        `json:"port2"`
	PortActive      int        `json:"port_active"`
	RecentLogs      []LogEntry `json:"recent_logs"`
	RegisteredAt    time.Time  `json:"registered_at"`
	Space           string     `json:"space"`
	WatchPatterns   []string   `json:"watch_patterns"`
}

type LogEntry struct {
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

type Status struct {
	AppCount        int    `json:"app_count"`
	PostgresPort    int    `json:"postgres_port"`
	PostgresRunning bool   `json:"postgres_running"`
	PostgresURL     string `json:"postgres_url"`
	Uptime          string `json:"uptime"`
}

type RegisterRequest struct {
	Config         []string `json:"config"`
	Dir            string   `json:"dir"`
	IgnorePatterns []string `json:"ignore_patterns"`
	Space          string   `json:"space"`
	WatchPatterns  []string `json:"watch_patterns"`
}

type RegisterResponse struct {
	DatabaseURL string `json:"database_url"`
	Port1       int    `json:"port1"`
	Port2       int    `json:"port2"`
	Space       string `json:"space"`
}
