package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"
)

type CmdResult struct {
	Err string
}

type Config struct {
	Run func(string, ...string) error
}

type Env struct {
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
	Stat     func(string) (os.FileInfo, error)
}

func EnvOr[T string | int](key string, fallback T) T {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	switch any(fallback).(type) {
	case string:
		return any(v).(T)
	case int:
		n, err := strconv.Atoi(v)
		if err != nil {
			return fallback
		}
		return any(n).(T)
	}
	return fallback
}

func DefaultConfig() Config {
	return Config{
		Run: func(name string, args ...string) error {
			cmd := exec.Command(name, args...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		},
	}
}

func DefaultEnv() Env {
	return Env{
		Getenv:   os.Getenv,
		ReadFile: os.ReadFile,
		Stat:     os.Stat,
	}
}

type Out struct {
	Env       map[string]string
	Providers []string
}

func Load(env Env, dir string, defaults ...map[string]string) Out {
	var defs map[string]string
	if len(defaults) > 0 {
		defs = defaults[0]
	}

	candidates := []string{".envrc"}
	if defs != nil {
		candidates = append(candidates, "main.go")
	}
	candidates = append(candidates, ".envrc.example")

	var strategies []string
	for _, f := range candidates {
		if _, err := env.Stat(filepath.Join(dir, f)); err == nil {
			strategies = append(strategies, f)
		}
	}

	vars := make(map[string]string, len(defs))

	if data, err := env.ReadFile(filepath.Join(dir, ".envrc.example")); err == nil {
		for k, v := range ParseExample(data) {
			if _, ok := defs[k]; ok {
				vars[k] = v
			}
		}
	}

	for k, v := range defs {
		if v != "" {
			vars[k] = v
		} else if _, ok := vars[k]; !ok {
			vars[k] = v
		}
	}

	for k := range vars {
		if e := env.Getenv(k); e != "" {
			vars[k] = e
		}
	}

	return Out{
		Env:       vars,
		Providers: strategies,
	}
}

func ParseExample(data []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

func TestConfig(cmds map[string]CmdResult) Config {
	return Config{
		Run: func(name string, args ...string) error {
			key := strings.Join(append([]string{name}, args...), " ")
			if r, ok := cmds[key]; ok {
				if r.Err != "" {
					return errors.New(r.Err)
				}
			}
			return nil
		},
	}
}

func TestEnv(vars map[string]string, files map[string]string) Env {
	return Env{
		Getenv: func(key string) string { return vars[key] },
		ReadFile: func(name string) ([]byte, error) {
			if content, ok := files[name]; ok {
				return []byte(content), nil
			}
			return nil, os.ErrNotExist
		},
		Stat: func(name string) (os.FileInfo, error) {
			if _, ok := files[name]; ok {
				return nil, nil
			}
			return nil, os.ErrNotExist
		},
	}
}

func Sync(cfg Config) error {
	if err := cfg.Run("direnv", "allow"); err != nil {
		return errors.Wrap(err, "direnv allow")
	}

	return nil
}
