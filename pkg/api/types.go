package api

import "time"

type App struct {
	Space              string     `json:"space"`
	Dir                string     `json:"dir"`
	ConfigFile         string     `json:"config_file"`
	DatabaseURL        string     `json:"database_url"`
	WatchPatterns      []string   `json:"watch_patterns"`
	IgnorePatterns     []string   `json:"ignore_patterns"`
	Port1              int        `json:"port1"`
	Port2              int        `json:"port2"`
	ActiveColor        string     `json:"active_color"`
	HealthStatus       string     `json:"health_status"`
	LastHealthCheck    time.Time  `json:"last_health_check"`
	RecentLogs         []LogEntry `json:"recent_logs"`
	RegisteredAt       time.Time  `json:"registered_at"`
}

type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

type Status struct {
	PostgresRunning bool   `json:"postgres_running"`
	PostgresPort    int    `json:"postgres_port"`
	PostgresURL     string `json:"postgres_url"`
	Uptime          string `json:"uptime"`
	AppCount        int    `json:"app_count"`
}

type RegisterRequest struct {
	Space          string   `json:"space"`
	Dir            string   `json:"dir"`
	ConfigFile     string   `json:"config_file"`
	WatchPatterns  []string `json:"watch_patterns"`
	IgnorePatterns []string `json:"ignore_patterns"`
}

type RegisterResponse struct {
	Space            string `json:"space"`
	Port1            int    `json:"port1"`
	Port2            int    `json:"port2"`
	DatabaseURL      string `json:"database_url"`
}
