package idempotency

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/funster-a/dd/internal/platform/idempotency/idempotencydb"
)

// flakyDB отказывает первые fails раз — как база во время переключения на
// реплику (ADR 028).
type flakyDB struct {
	fails int32
	calls atomic.Int32
}

func (f *flakyDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	if f.calls.Add(1) <= f.fails {
		return pgconn.CommandTag{}, errors.New("connection refused")
	}
	return pgconn.NewCommandTag("DELETE 1"), nil
}

func (f *flakyDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unused")
}
func (f *flakyDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func TestReleaseRetriesWhileDatabaseIsDown(t *testing.T) {
	releaseEvery, releaseFor = 5*time.Millisecond, time.Second
	t.Cleanup(func() { releaseEvery, releaseFor = 500*time.Millisecond, 30*time.Second })

	db := &flakyDB{fails: 3}
	release(t.Context(), idempotencydb.New(db), "scope", "key", slog.New(slog.DiscardHandler))
	deadline := time.Now().Add(time.Second)
	for db.calls.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := db.calls.Load(); got != 4 {
		t.Fatalf("release calls = %d, want 4 (three failures, then success)", got)
	}
	time.Sleep(30 * time.Millisecond)
	if got := db.calls.Load(); got != 4 {
		t.Errorf("release kept retrying after success: %d calls", got)
	}
}
