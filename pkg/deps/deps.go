package deps

import (
	"os"
	"os/exec"
	"strings"

	"github.com/cockroachdb/errors"
)

type CmdResult struct {
	Err string
}

type Config struct {
	Run func(string, ...string) error
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

func Sync(cfg Config) error {
	if err := cfg.Run("go", "mod", "tidy"); err != nil {
		return errors.Wrap(err, "go mod tidy")
	}

	if err := cfg.Run("go", "generate", "./..."); err != nil {
		return errors.Wrap(err, "go generate")
	}

	return nil
}
