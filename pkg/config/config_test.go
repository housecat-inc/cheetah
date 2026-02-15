package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/housecat-inc/cheetah/pkg/config"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		_name    string
		defaults map[string]string
		env      map[string]string
		files    map[string]string
		out      config.Out
	}{
		{
			_name:    "defaults only",
			defaults: map[string]string{"PORT": "8080"},
			out:      config.Out{Env: map[string]string{"PORT": "8080"}},
		},
		{
			_name:    "env overrides default",
			defaults: map[string]string{"PORT": "8080"},
			env:      map[string]string{"PORT": "9090"},
			out:      config.Out{Env: map[string]string{"PORT": "9090"}},
		},
		{
			_name:    "env missing uses default",
			defaults: map[string]string{"DATABASE_URL": "", "PORT": "8080"},
			env:      map[string]string{"PORT": "3000"},
			out:      config.Out{Env: map[string]string{"DATABASE_URL": "", "PORT": "3000"}},
		},
		{
			_name:    "example fills empty default",
			defaults: map[string]string{"DATABASE_URL": "", "PORT": "8080"},
			files:    map[string]string{".envrc.example": "export DATABASE_URL=postgres://localhost/dev\nexport PORT=3000"},
			out: config.Out{
				Env:       map[string]string{"DATABASE_URL": "postgres://localhost/dev", "PORT": "8080"},
				Providers: []string{".envrc.example"},
			},
		},
		{
			_name:    "default overrides example",
			defaults: map[string]string{"PORT": "8080"},
			files:    map[string]string{".envrc.example": "export PORT=3000"},
			out: config.Out{
				Env:       map[string]string{"PORT": "8080"},
				Providers: []string{".envrc.example"},
			},
		},
		{
			_name:    "env overrides default and example",
			defaults: map[string]string{"PORT": "8080"},
			env:      map[string]string{"PORT": "9090"},
			files:    map[string]string{".envrc.example": "export PORT=3000"},
			out: config.Out{
				Env:       map[string]string{"PORT": "9090"},
				Providers: []string{".envrc.example"},
			},
		},
		{
			_name:    "example key not in defaults ignored",
			defaults: map[string]string{"PORT": "8080"},
			files:    map[string]string{".envrc.example": "export SECRET=hunter2"},
			out:      config.Out{Env: map[string]string{"PORT": "8080"}},
		},
		{
			_name:    "example with quotes and comments",
			defaults: map[string]string{"A": "keep", "B": "", "C": ""},
			files:    map[string]string{".envrc.example": "# config\nexport A=\"hello\"\nexport B='world'\nC=plain"},
			out: config.Out{
				Env:       map[string]string{"A": "keep", "B": "world", "C": "plain"},
				Providers: []string{".envrc.example"},
			},
		},
		{
			_name: "empty files not providers",
			files: map[string]string{".envrc": "", ".envrc.example": ""},
			out:   config.Out{Env: map[string]string{}},
		},
		{
			_name:    "main.go provider when defaults contribute",
			defaults: map[string]string{"PORT": "8080"},
			files:    map[string]string{".envrc": "", "main.go": "", ".envrc.example": ""},
			out: config.Out{
				Env:       map[string]string{"PORT": "8080"},
				Providers: []string{"main.go"},
			},
		},
		{
			_name: "nil defaults skips main.go",
			files: map[string]string{".envrc": "", "main.go": "", ".envrc.example": ""},
			out:   config.Out{Env: map[string]string{}},
		},
		{
			_name:    "with defaults main.go missing",
			defaults: map[string]string{"PORT": "8080"},
			files:    map[string]string{".envrc": ""},
			out:      config.Out{Env: map[string]string{"PORT": "8080"}},
		},
		{
			_name: "envrc provides values",
			files: map[string]string{".envrc": "export GOOGLE_CLIENT_ID=abc123"},
			out: config.Out{
				Env:       map[string]string{"GOOGLE_CLIENT_ID": "abc123"},
				Providers: []string{".envrc"},
			},
		},
		{
			_name: "envrc overrides example",
			files: map[string]string{
				".envrc.example": "export PORT=3000",
				".envrc":         "export PORT=8080",
			},
			out: config.Out{
				Env:       map[string]string{"PORT": "8080"},
				Providers: []string{".envrc.example", ".envrc"},
			},
		},
		{
			_name: "envrc empty values not provider",
			files: map[string]string{".envrc": "export GOOGLE_CLIENT_ID=\"\""},
			out:   config.Out{Env: map[string]string{"GOOGLE_CLIENT_ID": ""}},
		},
		{
			_name:    "envrc overrides defaults",
			defaults: map[string]string{"PORT": "8080"},
			files:    map[string]string{".envrc": "export PORT=9090\nexport SECRET=xyz"},
			out: config.Out{
				Env:       map[string]string{"PORT": "9090", "SECRET": "xyz"},
				Providers: []string{".envrc"},
			},
		},
		{
			_name:    "no files exist",
			defaults: map[string]string{"PORT": "8080"},
			out:      config.Out{Env: map[string]string{"PORT": "8080"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)

			out := config.Load(config.TestEnv(tt.env, tt.files), "", tt.defaults)
			a.Equal(tt.out, out)
		})
	}
}

func TestParseExample(t *testing.T) {
	tests := []struct {
		_name string
		data  string
		out   map[string]string
	}{
		{
			_name: "export with value",
			data:  "export FOO=bar",
			out:   map[string]string{"FOO": "bar"},
		},
		{
			_name: "without export",
			data:  "FOO=bar",
			out:   map[string]string{"FOO": "bar"},
		},
		{
			_name: "double quotes",
			data:  `export FOO="bar baz"`,
			out:   map[string]string{"FOO": "bar baz"},
		},
		{
			_name: "single quotes",
			data:  "export FOO='bar baz'",
			out:   map[string]string{"FOO": "bar baz"},
		},
		{
			_name: "comments and blanks",
			data:  "# comment\n\nexport FOO=bar\n",
			out:   map[string]string{"FOO": "bar"},
		},
		{
			_name: "multiple vars",
			data:  "export A=1\nexport B=2",
			out:   map[string]string{"A": "1", "B": "2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)

			out := config.ParseExample([]byte(tt.data))
			a.Equal(tt.out, out)
		})
	}
}

func TestSync(t *testing.T) {
	tests := []struct {
		_name  string
		cmds   map[string]config.CmdResult
		envrc  bool
		err    string
	}{
		{
			_name: "no envrc skips direnv",
			envrc: false,
		},
		{
			_name: "envrc present success",
			envrc: true,
		},
		{
			_name: "direnv allow fails",
			envrc: true,
			cmds: map[string]config.CmdResult{
				"direnv allow": {Err: "exit status 1"},
			},
			err: "direnv allow",
		},
	}

	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)

			dir := t.TempDir()
			if tt.envrc {
				os.WriteFile(filepath.Join(dir, ".envrc"), nil, 0o644)
			}

			cfg := config.TestConfig(tt.cmds)
			err := config.Sync(cfg, dir)
			if tt.err != "" {
				a.ErrorContains(err, tt.err)
				return
			}
			a.NoError(err)
		})
	}
}
