package pg

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var adminURL string

func TestMain(m *testing.M) {
	pgURL, err := Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start embedded postgres: %v\n", err)
		os.Exit(1)
	}
	adminURL = pgURL
	os.Exit(m.Run())
}

func TestDBNameFromURL(t *testing.T) {
	tests := []struct {
		_name string
		in    string
		out   string
		err   bool
	}{
		{
			_name: "simple",
			in:    "postgres://localhost:5432/mydb",
			out:   "mydb",
		},
		{
			_name: "with credentials",
			in:    "postgres://user:pass@localhost:5432/app_dev?sslmode=disable",
			out:   "app_dev",
		},
		{
			_name: "postgres admin db",
			in:    "postgres://localhost/postgres",
			out:   "postgres",
		},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)
			out, err := DBName(tt.in)
			if tt.err {
				a.Error(err)
			} else {
				a.NoError(err)
				a.Equal(tt.out, out)
			}
		})
	}
}

func TestAdminURL(t *testing.T) {
	tests := []struct {
		_name string
		in    string
		out   string
	}{
		{
			_name: "replaces db name with postgres",
			in:    "postgres://user:pass@localhost:54320/myapp?sslmode=disable",
			out:   "postgres://user:pass@localhost:54320/postgres?sslmode=disable",
		},
		{
			_name: "already postgres",
			in:    "postgres://localhost:5432/postgres",
			out:   "postgres://localhost:5432/postgres",
		},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)
			out, err := AdminURL(tt.in)
			a.NoError(err)
			a.Equal(tt.out, out)
		})
	}
}

func TestHashMigrations(t *testing.T) {
	tests := []struct {
		_name string
		files map[string]string
		out   string
	}{
		{
			_name: "single file",
			files: map[string]string{
				"001_init.sql": "CREATE TABLE foo (id int);",
			},
		},
		{
			_name: "multiple files deterministic",
			files: map[string]string{
				"001_init.sql":  "CREATE TABLE foo (id int);",
				"002_users.sql": "CREATE TABLE users (id int);",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)
			dir := t.TempDir()
			for name, content := range tt.files {
				os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
			}

			hash1, err := Hash(dir)
			a.NoError(err)
			a.Len(hash1, 12)

			hash2, err := Hash(dir)
			a.NoError(err)
			a.Equal(hash1, hash2, "hash should be deterministic")
		})
	}
}

func TestHashMigrations_ContentChanges(t *testing.T) {
	a := assert.New(t)
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "001.sql"), []byte("v1"), 0o644)
	hash1, err := Hash(dir)
	a.NoError(err)

	os.WriteFile(filepath.Join(dir, "001.sql"), []byte("v2"), 0o644)
	hash2, err := Hash(dir)
	a.NoError(err)

	a.NotEqual(hash1, hash2, "hash should change when content changes")
}

func TestFindMigrationDir(t *testing.T) {
	tests := []struct {
		_name string
		setup func(dir string)
		out   string
		err   bool
	}{
		{
			_name: "sqlc.yaml with schema dir",
			setup: func(dir string) {
				os.WriteFile(filepath.Join(dir, "sqlc.yaml"), []byte("sql:\n  - schema: \"sql\"\n"), 0o644)
				os.MkdirAll(filepath.Join(dir, "sql"), 0o755)
			},
			out: "sql",
		},
		{
			_name: "fallback to migrations dir",
			setup: func(dir string) {
				os.MkdirAll(filepath.Join(dir, "migrations"), 0o755)
			},
			out: "migrations",
		},
		{
			_name: "no migration dir",
			setup: func(dir string) {},
			err:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)
			dir := t.TempDir()
			tt.setup(dir)

			out, err := MigrationDir(dir)
			if tt.err {
				a.Error(err)
			} else {
				a.NoError(err)
				a.Equal(filepath.Join(dir, tt.out), out)
			}
		})
	}
}

func TestHasSqlcConfig(t *testing.T) {
	tests := []struct {
		_name string
		setup func(dir string)
		out   bool
	}{
		{
			_name: "sqlc.yaml exists",
			setup: func(dir string) {
				os.WriteFile(filepath.Join(dir, "sqlc.yaml"), []byte("sql: []\n"), 0o644)
			},
			out: true,
		},
		{
			_name: "sqlc.yml exists",
			setup: func(dir string) {
				os.WriteFile(filepath.Join(dir, "sqlc.yml"), []byte("sql: []\n"), 0o644)
			},
			out: true,
		},
		{
			_name: "no config",
			setup: func(dir string) {},
			out:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)
			dir := t.TempDir()
			tt.setup(dir)
			a.Equal(tt.out, HasSqlcConfig(dir))
		})
	}
}

func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		_name string
		in    string
		out   string
	}{
		{
			_name: "simple",
			in:    "mydb",
			out:   `"mydb"`,
		},
		{
			_name: "with quotes",
			in:    `my"db`,
			out:   `"my""db"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)
			a.Equal(tt.out, quoteIdent(tt.in))
		})
	}
}

func TestTemplatePipeline(t *testing.T) {
	tests := []struct {
		_name      string
		sqlcYAML   string
		migrations map[string]string
		tables     []string
	}{
		{
			_name:    "single table",
			sqlcYAML: "sql:\n  - schema: \"sql\"\n",
			migrations: map[string]string{
				"001_init.sql": "-- +goose Up\nCREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL);\n-- +goose Down\nDROP TABLE items;\n",
			},
			tables: []string{"items"},
		},
		{
			_name:    "multiple tables",
			sqlcYAML: "sql:\n  - schema: \"sql\"\n",
			migrations: map[string]string{
				"001_users.sql":    "-- +goose Up\nCREATE TABLE users (id serial PRIMARY KEY, email text NOT NULL);\n-- +goose Down\nDROP TABLE users;\n",
				"002_projects.sql": "-- +goose Up\nCREATE TABLE projects (id serial PRIMARY KEY, name text NOT NULL, user_id int REFERENCES users(id));\n-- +goose Down\nDROP TABLE projects;\n",
			},
			tables: []string{"projects", "users"},
		},
		{
			_name: "migrations fallback dir",
			migrations: map[string]string{
				"001_widgets.sql": "-- +goose Up\nCREATE TABLE widgets (id serial PRIMARY KEY, label text NOT NULL);\n-- +goose Down\nDROP TABLE widgets;\n",
			},
			tables: []string{"widgets"},
		},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)
			r := require.New(t)

			dir := t.TempDir()

			if tt.sqlcYAML != "" {
				r.NoError(os.WriteFile(filepath.Join(dir, "sqlc.yaml"), []byte(tt.sqlcYAML), 0o644))
				r.NoError(os.MkdirAll(filepath.Join(dir, "sql"), 0o755))
				for name, content := range tt.migrations {
					r.NoError(os.WriteFile(filepath.Join(dir, "sql", name), []byte(content), 0o644))
				}
			} else {
				r.NoError(os.MkdirAll(filepath.Join(dir, "migrations"), 0o755))
				for name, content := range tt.migrations {
					r.NoError(os.WriteFile(filepath.Join(dir, "migrations", name), []byte(content), 0o644))
				}
			}

			migDir, err := MigrationDir(dir)
			r.NoError(err)

			hash, err := Hash(migDir)
			r.NoError(err)
			a.Len(hash, 12)

			tmplName, err := Template(adminURL, migDir, hash)
			r.NoError(err)
			a.Equal(prefix+hash, tmplName)

			tmplName2, err := Template(adminURL, migDir, hash)
			r.NoError(err)
			a.Equal(tmplName, tmplName2)

			appDB := fmt.Sprintf("test_%s_%s", t.Name(), hash)
			r.NoError(Create(adminURL, tmplName, appDB))
			t.Cleanup(func() { dropDB(adminURL, appDB) })

			appURL, err := replaceDBName(adminURL, appDB)
			r.NoError(err)

			db, err := sql.Open("postgres", appURL)
			r.NoError(err)
			defer db.Close()

			for _, table := range tt.tables {
				var exists bool
				err := db.QueryRow(
					"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)",
					table,
				).Scan(&exists)
				r.NoError(err)
				a.True(exists, "table %s should exist", table)
			}
		})
	}
}

func dropDB(pgURL, name string) {
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return
	}
	defer db.Close()
	db.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()", name)
	db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", quoteIdent(name)))
}
