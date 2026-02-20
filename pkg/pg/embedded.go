package pg

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cockroachdb/errors"
	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"github.com/housecat-inc/cheetah/pkg/config"
)

const startTimeout = 30 * time.Second

var port = config.EnvOr("PG_PORT", 54320)

func Run() (string, error) {
	url := fmt.Sprintf("postgres://postgres:postgres@localhost:%d/postgres?sslmode=disable", port)

	if dial() {
		return url, nil
	}

	dir := dir()
	lockPath := filepath.Join(dir, "postgres.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", errors.Wrap(err, "open lock file")
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return "", errors.Wrap(err, "acquire lock")
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	if dial() {
		return url, nil
	}

	slog.Info("starting embedded postgres", "port", port)

	logFile, err := os.OpenFile(filepath.Join(dir, "postgres.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", errors.Wrap(err, "open postgres log")
	}
	defer logFile.Close()

	db := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Port(uint32(port)).
			DataPath(filepath.Join(dir, fmt.Sprintf("pg-data-%d", port))).
			RuntimePath(filepath.Join(dir, fmt.Sprintf("pg-runtime-%d", port))).
			StartTimeout(startTimeout).
			Logger(logFile),
	)

	if err := db.Start(); err != nil {
		return "", errors.Wrap(err, "start postgres")
	}

	slog.Info("embedded postgres started", "port", port)
	return url, nil
}

func Stop(p int) {
	dataDir := filepath.Join(dir(), fmt.Sprintf("pg-data-%d", p))
	pidFile := filepath.Join(dataDir, "postmaster.pid")

	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}

	lines := strings.SplitN(string(data), "\n", 2)
	if len(lines) == 0 {
		return
	}

	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}

	slog.Info("stopping postgres", "port", p, "pid", pid)
	proc.Signal(syscall.SIGTERM)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	proc.Signal(syscall.SIGKILL)
}

func Dial() bool {
	return dial()
}

func dial() bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}

	dir := filepath.Join(home, ".cheetah")
	os.MkdirAll(dir, 0o755)
	return dir
}
