package cheetah

import (
	"os"
	"testing"

	"github.com/housecat-inc/cheetah/pkg/pg"
)

func TestDB(t testing.TB) string {
	tmplURL := os.Getenv("DATABASE_TEMPLATE_URL")
	if tmplURL == "" {
		t.Skip("DATABASE_TEMPLATE_URL not set")
	}

	dbURL, cleanup, err := pg.CreateTestDB(tmplURL)
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}
	t.Cleanup(cleanup)

	return dbURL
}
