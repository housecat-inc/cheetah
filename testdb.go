package cheetah

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cockroachdb/errors"

	"github.com/housecat-inc/cheetah/pkg/config"
	"github.com/housecat-inc/cheetah/pkg/pg"
)

var (
	tmplOnce sync.Once
	tmplURL  string
	tmplErr  error
)

func TestDB(t testing.TB) string {
	tmplOnce.Do(func() {
		port := config.EnvOr("PG_PORT", 54320)
		dbURL := fmt.Sprintf("postgres://postgres:postgres@localhost:%d/postgres?sslmode=disable", port)

		if _, err := pg.Run(); err != nil {
			tmplErr = errors.Wrap(err, "start postgres")
			return
		}

		tmplURL, tmplErr = pg.EnsureTemplate(dbURL)
	})
	if tmplErr != nil {
		t.Fatalf("ensure template db: %v", tmplErr)
	}

	testURL, cleanup, err := pg.CreateTestDB(tmplURL)
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}
	t.Cleanup(cleanup)

	return testURL
}
