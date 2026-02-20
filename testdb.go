package cheetah

import (
	"os"
	"testing"

	"github.com/housecat-inc/cheetah/pkg/pg"
)

func TestDB(t testing.TB) string {
	tmplURL := os.Getenv("DATABASE_TEMPLATE_URL")
	if tmplURL == "" {
		dbURL := os.Getenv("DATABASE_URL")
		if dbURL == "" {
			t.Skip("DATABASE_TEMPLATE_URL and DATABASE_URL not set")
		}

		var err error
		tmplURL, err = pg.Ensure(dbURL)
		if err != nil {
			t.Fatalf("ensure template db: %v", err)
		}
	}

	dbURL, cleanup, err := pg.CreateTestDB(tmplURL)
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}
	t.Cleanup(cleanup)

	return dbURL
}
