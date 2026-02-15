package db

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"gopkg.in/yaml.v3"
)

// sqlcConfig is a minimal representation of sqlc.yaml.
type sqlcConfig struct {
	SQL []struct {
		Schema string `yaml:"schema"`
	} `yaml:"sql"`
}

// FindMigrationDir discovers the migration directory from sqlc.yaml/yml in dir.
// Falls back to "migrations/" if no sqlc config exists but the directory is present.
// Returns an error if no migration directory can be found.
func FindMigrationDir(dir string) (string, error) {
	for _, name := range []string{"sqlc.yaml", "sqlc.yml"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg sqlcConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return "", fmt.Errorf("parse %s: %w", name, err)
		}
		if len(cfg.SQL) > 0 && cfg.SQL[0].Schema != "" {
			schema := filepath.Join(dir, cfg.SQL[0].Schema)
			if info, err := os.Stat(schema); err == nil && info.IsDir() {
				return schema, nil
			}
		}
	}

	// Fallback: check for migrations/ directory
	fallback := filepath.Join(dir, "migrations")
	if info, err := os.Stat(fallback); err == nil && info.IsDir() {
		return fallback, nil
	}

	return "", fmt.Errorf("no migration directory found in %s", dir)
}

// HasSqlcConfig returns true if sqlc.yaml or sqlc.yml exists in dir.
func HasSqlcConfig(dir string) bool {
	for _, name := range []string{"sqlc.yaml", "sqlc.yml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// HashMigrations computes a SHA256 hash of all *.sql files in dir, sorted by name.
// Returns a 12-character hex string.
func HashMigrations(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read migration dir: %w", err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	h := sha256.New()
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		h.Write([]byte(name))
		h.Write(data)
	}

	return fmt.Sprintf("%x", h.Sum(nil))[:12], nil
}

// EnsureTemplate creates a template database named tmpl_{hash} if it doesn't
// already exist, then runs goose migrations on it. Returns the template DB name.
// pgURL should point to the postgres admin database (e.g. postgres://localhost:54320/postgres).
func EnsureTemplate(pgURL string, migrationsDir string, hash string) (string, error) {
	tmplName := "tmpl_" + hash

	adminDB, err := sql.Open("postgres", pgURL)
	if err != nil {
		return "", fmt.Errorf("connect to admin db: %w", err)
	}
	defer adminDB.Close()

	// Check if template DB already exists
	var exists bool
	err = adminDB.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", tmplName).Scan(&exists)
	if err != nil {
		return "", fmt.Errorf("check template db: %w", err)
	}
	if exists {
		return tmplName, nil
	}

	// Create template DB
	slog.Info("creating template db", "name", tmplName)
	if _, err := adminDB.Exec(fmt.Sprintf("CREATE DATABASE %s", quoteIdent(tmplName))); err != nil {
		return "", fmt.Errorf("create template db: %w", err)
	}

	// Connect to template DB and run migrations
	tmplURL, err := replaceDBName(pgURL, tmplName)
	if err != nil {
		return "", err
	}
	tmplDB, err := sql.Open("postgres", tmplURL)
	if err != nil {
		return "", fmt.Errorf("connect to template db: %w", err)
	}
	defer tmplDB.Close()

	goose.SetDialect("postgres")
	if err := goose.Up(tmplDB, migrationsDir); err != nil {
		return "", fmt.Errorf("run migrations: %w", err)
	}

	return tmplName, nil
}

// CloneDB drops targetDB if it exists (terminating connections), then creates
// it from templateDB using CREATE DATABASE ... TEMPLATE.
// pgURL should point to the postgres admin database.
func CloneDB(pgURL string, templateDB string, targetDB string) error {
	adminDB, err := sql.Open("postgres", pgURL)
	if err != nil {
		return fmt.Errorf("connect to admin db: %w", err)
	}
	defer adminDB.Close()

	// Terminate connections to target DB
	adminDB.Exec(
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()",
		targetDB,
	)

	// Drop if exists
	if _, err := adminDB.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", quoteIdent(targetDB))); err != nil {
		return fmt.Errorf("drop db: %w", err)
	}

	// Create from template
	if _, err := adminDB.Exec(fmt.Sprintf(
		"CREATE DATABASE %s TEMPLATE %s",
		quoteIdent(targetDB), quoteIdent(templateDB),
	)); err != nil {
		return fmt.Errorf("clone db: %w", err)
	}

	return nil
}

// AdminURL returns the admin postgres URL (connecting to the "postgres" database)
// from any database URL on the same server.
func AdminURL(dbURL string) (string, error) {
	return replaceDBName(dbURL, "postgres")
}

// DBNameFromURL extracts the database name from a postgres URL.
func DBNameFromURL(dbURL string) (string, error) {
	u, err := url.Parse(dbURL)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	return strings.TrimPrefix(u.Path, "/"), nil
}

// replaceDBName replaces the database name in a postgres URL.
func replaceDBName(pgURL string, dbName string) (string, error) {
	u, err := url.Parse(pgURL)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	u.Path = "/" + dbName
	return u.String(), nil
}

// quoteIdent quotes a PostgreSQL identifier to prevent injection.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
