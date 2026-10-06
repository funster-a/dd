package db_test

import (
	"os"
	"testing"

	"github.com/funster-a/dd/internal/platform/db"
)

// Реплики недоступны — чтения идут с ведущего узла (ADR 031): отказ реплик
// возвращает нагрузку на ведущий, но не ломает страницы.
func TestReaderFallsBackToPrimary(t *testing.T) {
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	primary, err := db.NewPool(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(primary.Close)
	dead, err := db.NewPool(t.Context(), "postgres://dd:dd@127.0.0.1:1/dd?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dead.Close)

	for name, r := range map[string]*db.Reader{
		"replica down": db.NewReader(dead, primary),
		"no replica":   db.NewReader(nil, primary),
		"replica up":   db.NewReader(primary, primary),
	} {
		var n int
		if err := r.QueryRow(t.Context(), "SELECT 41 + 1").Scan(&n); err != nil || n != 42 {
			t.Errorf("%s: QueryRow = %d, %v", name, n, err)
		}
		rows, err := r.Query(t.Context(), "SELECT generate_series(1, 3)")
		if err != nil {
			t.Fatalf("%s: Query: %v", name, err)
		}
		count := 0
		for rows.Next() {
			count++
		}
		if rows.Err() != nil || count != 3 {
			t.Errorf("%s: Query rows = %d, %v", name, count, rows.Err())
		}
	}
}
