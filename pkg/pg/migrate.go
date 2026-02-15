package pg

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

	"github.com/cockroachdb/errors"
	_ "github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"gopkg.in/yaml.v3"
)

const prefix = "t_"

func Hash(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", errors.Wrap(err, "read migration dir")
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
			return "", errors.Wrapf(err, "read %s", name)
		}
		h.Write([]byte(name))
		h.Write(data)
	}

	return fmt.Sprintf("%x", h.Sum(nil))[:12], nil
}

// Template creates a template database named tmpl_{hash} if it doesn't
// already exist, then runs goose migrations on it. Returns the template DB name.
func Template(adminURL string, dir string, hash string) (string, error) {
	name := prefix + hash

	adminDB, err := sql.Open("postgres", adminURL)
	if err != nil {
		return "", errors.Wrap(err, "connect to admin db")
	}
	defer adminDB.Close()

	var exists bool
	err = adminDB.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists)
	if err != nil {
		return "", errors.Wrap(err, "check template db")
	}
	if exists {
		return name, nil
	}

	slog.Info("creating template db", "name", name)
	if _, err := adminDB.Exec(fmt.Sprintf("CREATE DATABASE %s", quoteIdent(name))); err != nil {
		return "", errors.Wrap(err, "create template db")
	}

	tmplURL, err := replaceDBName(adminURL, name)
	if err != nil {
		return "", errors.Wrap(err, "replace db name")
	}

	tmplDB, err := sql.Open("postgres", tmplURL)
	if err != nil {
		return "", errors.Wrap(err, "connect to template db")
	}
	defer tmplDB.Close()

	goose.SetDialect("postgres")
	if err := goose.Up(tmplDB, dir); err != nil {
		return "", errors.Wrap(err, "run migrations")
	}

	return name, nil
}

// Create drops targetDB if it exists (terminating connections), then creates
// it from templateDB using CREATE DATABASE ... TEMPLATE.
func Create(adminURL string, templateDB string, targetDB string) error {
	db, err := sql.Open("postgres", adminURL)
	if err != nil {
		return errors.Wrap(err, "connect to admin db")
	}
	defer db.Close()

	db.Exec(
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()",
		targetDB,
	)

	if _, err := db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", quoteIdent(targetDB))); err != nil {
		return errors.Wrap(err, "drop db")
	}

	if _, err := db.Exec(fmt.Sprintf(
		"CREATE DATABASE %s TEMPLATE %s",
		quoteIdent(targetDB), quoteIdent(templateDB),
	)); err != nil {
		return errors.Wrap(err, "clone db")
	}

	return nil
}

func AdminURL(dbURL string) (string, error) {
	return replaceDBName(dbURL, "postgres")
}

func DBName(dbURL string) (string, error) {
	u, err := url.Parse(dbURL)
	if err != nil {
		return "", errors.Wrap(err, "parse url")
	}
	return strings.TrimPrefix(u.Path, "/"), nil
}

func replaceDBName(pgURL string, dbName string) (string, error) {
	u, err := url.Parse(pgURL)
	if err != nil {
		return "", errors.Wrap(err, "parse url")
	}
	u.Path = "/" + dbName
	return u.String(), nil
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

type sqlcConfig struct {
	SQL []struct {
		Schema string `yaml:"schema"`
	} `yaml:"sql"`
}

func MigrationDir(dir string) (string, error) {
	for _, name := range []string{"sqlc.yaml", "sqlc.yml"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg sqlcConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return "", errors.Wrapf(err, "parse %s", name)
		}
		if len(cfg.SQL) > 0 && cfg.SQL[0].Schema != "" {
			schema := filepath.Join(dir, cfg.SQL[0].Schema)
			if info, err := os.Stat(schema); err == nil && info.IsDir() {
				return schema, nil
			}
		}
	}

	fallback := filepath.Join(dir, "migrations")
	if info, err := os.Stat(fallback); err == nil && info.IsDir() {
		return fallback, nil
	}

	return "", errors.Newf("no migration directory found in %s", dir)
}

func HasSqlcConfig(dir string) bool {
	for _, name := range []string{"sqlc.yaml", "sqlc.yml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}
