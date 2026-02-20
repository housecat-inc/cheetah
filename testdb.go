package cheetah

import (
	"fmt"
	"testing"

	"github.com/housecat-inc/cheetah/pkg/config"
	"github.com/housecat-inc/cheetah/pkg/pg"
)

func TestDB(t testing.TB) string {
	port := config.EnvOr("PG_PORT", 54320)
	dbURL := fmt.Sprintf("postgres://postgres:postgres@localhost:%d/postgres?sslmode=disable", port)

	tmplURL, err := pg.Ensure(dbURL)
	if err != nil {
		t.Fatalf("ensure template db: %v", err)
	}

	testURL, cleanup, err := pg.CreateTestDB(tmplURL)
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}
	t.Cleanup(cleanup)

	return testURL
}
