package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/housecat-inc/spacecat/pkg/config"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		_name    string
		defaults map[string]string
		env      map[string]string
		example  string
		out      map[string]string
	}{
		{
			_name:    "defaults only",
			defaults: map[string]string{"PORT": "8080"},
			out:      map[string]string{"PORT": "8080"},
		},
		{
			_name:    "env overrides default",
			defaults: map[string]string{"PORT": "8080"},
			env:      map[string]string{"PORT": "9090"},
			out:      map[string]string{"PORT": "9090"},
		},
		{
			_name:    "env missing uses default",
			defaults: map[string]string{"DATABASE_URL": "", "PORT": "8080"},
			env:      map[string]string{"PORT": "3000"},
			out:      map[string]string{"DATABASE_URL": "", "PORT": "3000"},
		},
		{
			_name:    "default overrides example",
			defaults: map[string]string{"PORT": "8080"},
			example:  "export PORT=3000",
			out:      map[string]string{"PORT": "8080"},
		},
		{
			_name:    "example fills empty default",
			defaults: map[string]string{"DATABASE_URL": "", "PORT": "8080"},
			example:  "export DATABASE_URL=postgres://localhost/dev\nexport PORT=3000",
			out:      map[string]string{"DATABASE_URL": "postgres://localhost/dev", "PORT": "8080"},
		},
		{
			_name:    "env overrides default and example",
			defaults: map[string]string{"PORT": "8080"},
			env:      map[string]string{"PORT": "9090"},
			example:  "export PORT=3000",
			out:      map[string]string{"PORT": "9090"},
		},
		{
			_name:    "example key not in defaults ignored",
			defaults: map[string]string{"PORT": "8080"},
			example:  "export SECRET=hunter2",
			out:      map[string]string{"PORT": "8080"},
		},
		{
			_name:    "example with quotes and comments",
			defaults: map[string]string{"A": "keep", "B": "", "C": ""},
			example:  "# config\nexport A=\"hello\"\nexport B='world'\nC=plain",
			out:      map[string]string{"A": "keep", "B": "world", "C": "plain"},
		},
	}

	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)

			env := config.TestEnv(tt.env)
			if tt.example != "" {
				env.ReadFile = func(string) ([]byte, error) { return []byte(tt.example), nil }
			}
			out := config.Load(env, ".envrc.example", tt.defaults)
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
		_name string
		cmds  map[string]config.CmdResult
		err   string
	}{
		{
			_name: "success",
		},
		{
			_name: "direnv allow fails",
			cmds: map[string]config.CmdResult{
				"direnv allow": {Err: "exit status 1"},
			},
			err: "direnv allow",
		},
	}

	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)

			cfg := config.TestConfig(tt.cmds)
			err := config.Sync(cfg)
			if tt.err != "" {
				a.ErrorContains(err, tt.err)
				return
			}
			a.NoError(err)
		})
	}
}
