package cheetah

import (
	"os"
	"testing"

	"github.com/housecat-inc/cheetah/pkg/pg"
)

func TestDB(t testing.TB) string {
	testURL := os.Getenv("DATABASE_TEMPLATE_URL")
	if testURL == "" {
		t.Skip("DATABASE_TEMPLATE_URL not set")
	}

	dbURL, cleanup, err := pg.CreateTestDB(testURL)
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}
	t.Cleanup(cleanup)

	return dbURL
}
