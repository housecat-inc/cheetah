package build

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/cockroachdb/errors"
)

type In struct {
	AppEnv      map[string]string
	DatabaseURL string
	Port        int
	Space       string
	CheetahURL string
}

type Out struct {
	Cmd *exec.Cmd
}

func Generate() error {
	gen := exec.Command("go", "generate", "./...")
	gen.Stdout = os.Stdout
	gen.Stderr = os.Stderr
	if err := gen.Run(); err != nil {
		return errors.Wrap(err, "generate")
	}
	return nil
}

func Run(in In) (Out, error) {
	binPath := filepath.Join(".cheetah", "app")
	os.MkdirAll(".cheetah", 0o755)

	if err := Generate(); err != nil {
		return Out{}, err
	}

	b := exec.Command("go", "build", "-o", binPath, "./cmd/app")
	b.Stdout = os.Stdout
	b.Stderr = os.Stderr
	b.Env = append(os.Environ(),
		fmt.Sprintf("DATABASE_URL=%s", in.DatabaseURL),
	)
	if err := b.Run(); err != nil {
		return Out{}, errors.Wrap(err, "build")
	}

	cmd := exec.Command(binPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	for k, v := range in.AppEnv {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = append(cmd.Env,
		fmt.Sprintf("DATABASE_URL=%s", in.DatabaseURL),
		fmt.Sprintf("PORT=%d", in.Port),
		fmt.Sprintf("SPACE=%s", in.Space),
		fmt.Sprintf("CHEETAH_URL=%s", in.CheetahURL),
	)
	if err := cmd.Start(); err != nil {
		return Out{}, errors.Wrap(err, "run")
	}

	slog.Info("server", "port", in.Port, "pid", cmd.Process.Pid)
	return Out{Cmd: cmd}, nil
}
