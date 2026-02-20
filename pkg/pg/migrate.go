package pg

import (
	"crypto/rand"
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
	migrate "github.com/rubenv/sql-migrate"
	"gopkg.in/yaml.v3"
)

const prefix = "t_"

func Ensure(databaseURL string) (string, error) {
	migDirs, err := MigrationDirs(".")
	if err != nil {
		return "", nil
	}

	hash, err := Hash(migDirs)
	if err != nil {
		return "", errors.Wrap(err, "hash migrations")
	}

	adminURL, err := AdminURL(databaseURL)
	if err != nil {
		return "", errors.Wrap(err, "admin url")
	}

	tmplName, err := Template(adminURL, migDirs, hash)
	if err != nil {
		return "", errors.Wrap(err, "ensure template")
	}

	appDBName, err := DBName(databaseURL)
	if err != nil {
		return "", errors.Wrap(err, "db name")
	}

	if err := Create(adminURL, tmplName, appDBName); err != nil {
		return "", errors.Wrap(err, "clone db")
	}

	tmplURL, err := replaceDBName(databaseURL, tmplName)
	if err != nil {
		return "", errors.Wrap(err, "template url")
	}

	slog.Info("database", "template", tmplName, "database_url", databaseURL)
	return tmplURL, nil
}

func Hash(paths []string) (string, error) {
	h := sha256.New()
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return "", errors.Wrapf(err, "stat %s", p)
		}
		if !info.IsDir() {
			data, err := os.ReadFile(p)
			if err != nil {
				return "", errors.Wrapf(err, "read %s", p)
			}
			h.Write([]byte(filepath.Base(p)))
			h.Write(data)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return "", errors.Wrapf(err, "read dir %s", p)
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			data, err := os.ReadFile(filepath.Join(p, name))
			if err != nil {
				return "", errors.Wrapf(err, "read %s", name)
			}
			h.Write([]byte(name))
			h.Write(data)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:12], nil
}

// Template creates a template database named tmpl_{hash} if it doesn't
// already exist, then runs migrations on it. Returns the template DB name.
func Template(adminURL string, dirs []string, hash string) (string, error) {
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

	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil {
			dropDB(adminDB, name)
			return "", errors.Wrapf(err, "stat %s", dir)
		}
		if !info.IsDir() {
			continue
		}
		if err := runMigrations(tmplDB, dir); err != nil {
			tmplDB.Close()
			dropDB(adminDB, name)
			return "", errors.Wrapf(err, "run migrations in %s", dir)
		}
	}

	return name, nil
}

func migrationFormat(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "goose"
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		content := string(data)
		if strings.Contains(content, "-- +goose") {
			return "goose"
		}
		if strings.Contains(content, "-- +migrate") {
			return "sql-migrate"
		}
	}
	return "goose"
}

func runMigrations(db *sql.DB, dir string) error {
	switch migrationFormat(dir) {
	case "sql-migrate":
		_, err := migrate.Exec(db, "postgres", &migrate.FileMigrationSource{Dir: dir}, migrate.Up)
		return err
	default:
		goose.SetDialect("postgres")
		return goose.Up(db, dir)
	}
}

func dropDB(adminDB *sql.DB, name string) {
	adminDB.Exec(
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()",
		name,
	)
	adminDB.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", quoteIdent(name)))
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
		Schema interface{} `yaml:"schema"`
	} `yaml:"sql"`
}

func MigrationDirs(dir string) ([]string, error) {
	var paths []string
	seen := map[string]bool{}

	for _, cfgPath := range findSqlcConfigs(dir) {
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			continue
		}
		var cfg sqlcConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, errors.Wrapf(err, "parse %s", filepath.Base(cfgPath))
		}
		cfgDir := filepath.Dir(cfgPath)
		for _, entry := range cfg.SQL {
			for _, s := range schemaPaths(entry.Schema) {
				p := filepath.Join(cfgDir, s)
				if _, err := os.Stat(p); err == nil && !seen[p] {
					seen[p] = true
					paths = append(paths, p)
				}
			}
		}
	}

	if len(paths) == 0 {
		fallback := filepath.Join(dir, "migrations")
		if info, err := os.Stat(fallback); err == nil && info.IsDir() {
			return []string{fallback}, nil
		}
		return nil, errors.Newf("no migration directory found in %s", dir)
	}

	return paths, nil
}

func HasSqlcConfig(dir string) bool {
	return len(findSqlcConfigs(dir)) > 0
}

func findSqlcConfigs(dir string) []string {
	var configs []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == "sqlc.yaml" || name == "sqlc.yml" {
			configs = append(configs, path)
		}
		return nil
	})
	sort.Strings(configs)
	return configs
}

func schemaPaths(v interface{}) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []interface{}:
		var paths []string
		for _, p := range v {
			if s, ok := p.(string); ok {
				paths = append(paths, s)
			}
		}
		return paths
	}
	return nil
}

func CreateTestDB(templateURL string) (string, func(), error) {
	adminURL, err := AdminURL(templateURL)
	if err != nil {
		return "", nil, errors.Wrap(err, "admin url")
	}

	tmplName, err := DBName(templateURL)
	if err != nil {
		return "", nil, errors.Wrap(err, "template name")
	}

	b := make([]byte, 6)
	rand.Read(b)
	targetName := fmt.Sprintf("test_%x", b)

	if err := Create(adminURL, tmplName, targetName); err != nil {
		return "", nil, errors.Wrap(err, "create test db")
	}

	dbURL, err := replaceDBName(templateURL, targetName)
	if err != nil {
		return "", nil, errors.Wrap(err, "build url")
	}

	cleanup := func() {
		Drop(dbURL)
	}

	return dbURL, cleanup, nil
}

func Drop(databaseURL string) error {
	adminURL, err := AdminURL(databaseURL)
	if err != nil {
		return errors.Wrap(err, "admin url")
	}

	dbName, err := DBName(databaseURL)
	if err != nil {
		return errors.Wrap(err, "db name")
	}

	db, err := sql.Open("postgres", adminURL)
	if err != nil {
		return errors.Wrap(err, "connect")
	}
	defer db.Close()

	dropDB(db, dbName)
	return nil
}
