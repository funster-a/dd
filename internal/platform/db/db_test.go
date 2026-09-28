package db_test

import (
	"os"
	"testing"
	"time"

	"github.com/funster-a/dd/internal/platform/db"
)

// TestTimestamptzIsUTC: время из базы приходит в UTC даже при другом
// часовом поясе процесса (правило 6).
func TestTimestamptzIsUTC(t *testing.T) {
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	orig := time.Local
	time.Local = time.FixedZone("ALMT", 5*3600)
	t.Cleanup(func() { time.Local = orig })

	pool, err := db.NewPool(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var ts time.Time
	if err := pool.QueryRow(t.Context(), `SELECT '2026-10-01 12:00:00+00'::timestamptz`).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if ts.Location() != time.UTC || ts.Hour() != 12 {
		t.Fatalf("got %v (%s), want 12:00 UTC", ts, ts.Location())
	}
}
