package config

import (
	"os"
	"os/exec"
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
}

func EnvOrInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
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
	}
}

func Load(env Env, exampleFile string, defaults map[string]string) map[string]string {
	out := make(map[string]string, len(defaults))

	if data, err := env.ReadFile(exampleFile); err == nil {
		for k, v := range ParseExample(data) {
			if _, ok := defaults[k]; ok {
				out[k] = v
			}
		}
	}

	for k, v := range defaults {
		if v != "" {
			out[k] = v
		} else if _, ok := out[k]; !ok {
			out[k] = v
		}
	}

	for k := range out {
		if e := env.Getenv(k); e != "" {
			out[k] = e
		}
	}

	return out
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

func TestEnv(vars map[string]string) Env {
	return Env{
		Getenv:   func(key string) string { return vars[key] },
		ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
	}
}

func Sync(cfg Config) error {
	if err := cfg.Run("direnv", "allow"); err != nil {
		return errors.Wrap(err, "direnv allow")
	}

	return nil
}
