package postgres

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

const (
	port         = 54320
	startTimeout = 30 * time.Second
)

// EnsureRunning checks whether Postgres is already listening on :54320.
// If not, it acquires a cross-process file lock and starts an embedded
// Postgres instance. The instance runs as an independent daemon — it is
// never stopped by this function. Data persists in ~/.spacecat/pg-data/.
func EnsureRunning() (string, error) {
	pgURL := fmt.Sprintf("postgres://postgres:postgres@localhost:%d/postgres?sslmode=disable", port)

	// Fast path: already running
	if isRunning() {
		return pgURL, nil
	}

	// Acquire cross-process lock
	dir := spacecatDir()
	lockPath := filepath.Join(dir, "postgres.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", fmt.Errorf("open lock file: %w", err)
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return "", fmt.Errorf("acquire lock: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	// Re-check after acquiring lock (another process may have started it)
	if isRunning() {
		return pgURL, nil
	}

	slog.Info("starting embedded postgres", "port", port)

	logFile, err := os.OpenFile(filepath.Join(dir, "postgres.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", fmt.Errorf("open postgres log: %w", err)
	}
	defer logFile.Close()

	pg := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Port(uint32(port)).
			DataPath(filepath.Join(dir, "pg-data")).
			RuntimePath(filepath.Join(dir, "pg-runtime")).
			StartTimeout(startTimeout).
			Logger(logFile),
	)

	if err := pg.Start(); err != nil {
		return "", fmt.Errorf("start postgres: %w", err)
	}

	slog.Info("embedded postgres started", "port", port)
	return pgURL, nil
}

// isRunning checks whether something is listening on the Postgres port.
func isRunning() bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// spacecatDir returns ~/.spacecat/, creating it if necessary.
func spacecatDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	dir := filepath.Join(home, ".spacecat")
	os.MkdirAll(dir, 0o755)
	return dir
}
