package space

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cockroachdb/errors"
)

type Config struct {
	CmdOutput func(string, ...string) (string, error)
	Getenv    func(string) string
	Getwd     func() (string, error)
}

type Out struct {
	Dir  string
	Name string
}

func DefaultConfig() Config {
	return Config{
		CmdOutput: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).Output()
			return string(out), errors.Wrap(err, "")
		},
		Getenv: os.Getenv,
		Getwd:  os.Getwd,
	}
}

type CmdResult struct {
	Err string
	Out string
}

func TestConfig(cmds map[string]CmdResult, dir string, env []string) Config {
	return Config{
		CmdOutput: func(name string, args ...string) (string, error) {
			cmd := strings.Join(append([]string{name}, args...), " ")
			if r, ok := cmds[cmd]; ok {
				if r.Err != "" {
					return "", fmt.Errorf("%s", r.Err)
				}
				return r.Out, nil
			}
			return "", nil
		},
		Getenv: func(key string) string {
			for _, e := range env {
				k, v, _ := strings.Cut(e, "=")
				if k == key {
					return v
				}
			}
			return ""
		},
		Getwd: func() (string, error) { return dir, nil },
	}
}

func Default() (Out, error) {
	return Space(DefaultConfig())
}

func Space(cfg Config) (Out, error) {
	var name string

	if s := cfg.Getenv("SPACE"); s != "" {
		name = s
	} else if s := cfg.Getenv("CONDUCTOR_SPACE"); s != "" {
		name = s
	} else {
		out, err := cfg.CmdOutput("git", "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return Out{}, errors.Wrap(err, "set SPACE or CONDUCTOR_SPACE env var, or run in a git repo")
		}
		branch := strings.TrimSpace(out)
		name = strings.ReplaceAll(branch, "/", "-")
	}

	dir, err := cfg.Getwd()
	if err != nil {
		return Out{}, errors.Wrap(err, "getwd")
	}

	return Out{Dir: dir, Name: name}, nil
}
