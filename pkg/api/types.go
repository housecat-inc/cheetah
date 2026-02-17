package api

import "time"

type App struct {
	Config      []string  `json:"config"`
	CreatedAt   time.Time `json:"created_at"`
	DatabaseURL string    `json:"database_url"`
	Dir         string    `json:"dir"`
	Health      Health    `json:"health"`
	Logs        []Log     `json:"logs"`
	Ports       Ports     `json:"ports"`
	Space       string    `json:"space"`
	Watch       Watch     `json:"watch"`
}

type Health struct {
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Ports struct {
	Active int `json:"active"`
	Blue   int `json:"blue"`
	Green  int `json:"green"`
}

type Watch struct {
	Ignore []string `json:"ignore"`
	Match  []string `json:"match"`
}

type Log struct {
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
	Version         string `json:"version"`
}

type AppIn struct {
	Config []string `json:"config"`
	Dir    string   `json:"dir"`
	Space  string   `json:"space"`
	Watch  Watch    `json:"watch"`
}

type AppOut struct {
	DatabaseURL string            `json:"database_url"`
	Env         map[string]string `json:"env,omitempty"`
	Ports       Ports             `json:"ports"`
	Space       string            `json:"space"`
}

type EnvExportIn struct {
	App        string `json:"app"`
	Passphrase string `json:"passphrase"`
}

type EnvExportOut struct {
	Blob string `json:"blob"`
}

type EnvImportIn struct {
	Blob       string `json:"blob"`
	Passphrase string `json:"passphrase"`
}

type EnvImportOut struct {
	App  string            `json:"app"`
	Vars map[string]string `json:"vars"`
}
