package outbox

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/platform/db/dbtest"
)

type fakePublisher struct {
	mu     sync.Mutex
	got    []string // очередь:тело
	failAt int      // номер публикации (с 1), которая падает; 0 — без сбоев
	n      int
}

func (p *fakePublisher) Publish(_ context.Context, queue string, deadLetter bool, _ string, body []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	if p.n == p.failAt {
		return errors.New("broker is down")
	}
	if !deadLetter {
		return errors.New("events must be published with a dead-letter queue")
	}
	p.got = append(p.got, queue+":"+string(body))
	return nil
}

func add(t *testing.T, db *dbtest.DB, topics ...string) {
	t.Helper()
	err := pgx.BeginFunc(t.Context(), db.Pool, func(tx pgx.Tx) error {
		for i, topic := range topics {
			if err := Add(t.Context(), tx, topic, map[string]int{"n": i}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func unpublished(t *testing.T, db *dbtest.DB) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRelayPublishesInOrder(t *testing.T) {
	db := dbtest.New(t)
	add(t, db, "a.b", "c.d", "a.b")
	pub := &fakePublisher{}
	r := NewRelay(db.Pool, pub, "test.", slog.New(slog.DiscardHandler))
	n, err := r.RelayOnce(t.Context())
	if err != nil || n != 3 {
		t.Fatalf("RelayOnce = %d, %v; want 3", n, err)
	}
	want := []string{`test.a.b:{"n": 0}`, `test.c.d:{"n": 1}`, `test.a.b:{"n": 2}`}
	for i := range want {
		if pub.got[i] != want[i] {
			t.Errorf("published[%d] = %s, want %s", i, pub.got[i], want[i])
		}
	}
	if u := unpublished(t, db); u != 0 {
		t.Errorf("unpublished = %d, want 0", u)
	}
	if n, _ := r.RelayOnce(t.Context()); n != 0 {
		t.Errorf("second RelayOnce = %d, want 0", n)
	}
}

// Событие из откатившейся транзакции не публикуется: outbox пишется
// вместе с данными или не пишется вовсе.
func TestRolledBackEventIsNotPublished(t *testing.T) {
	db := dbtest.New(t)
	_ = pgx.BeginFunc(t.Context(), db.Pool, func(tx pgx.Tx) error {
		if err := Add(t.Context(), tx, "a.b", 1); err != nil {
			t.Fatal(err)
		}
		return errors.New("business rule failed")
	})
	if u := unpublished(t, db); u != 0 {
		t.Errorf("unpublished = %d after rollback, want 0", u)
	}
}

// Брокер упал на середине пачки: опубликованные помечаются, остальные
// уходят следующим проходом.
func TestRelayPartialFailure(t *testing.T) {
	db := dbtest.New(t)
	add(t, db, "a", "b", "c")
	pub := &fakePublisher{failAt: 2}
	r := NewRelay(db.Pool, pub, "", slog.New(slog.DiscardHandler))
	n, err := r.RelayOnce(t.Context())
	if err == nil || n != 1 {
		t.Fatalf("RelayOnce = %d, %v; want 1 and an error", n, err)
	}
	if u := unpublished(t, db); u != 2 {
		t.Fatalf("unpublished = %d, want 2", u)
	}
	if n, err := r.RelayOnce(t.Context()); err != nil || n != 2 {
		t.Fatalf("retry = %d, %v; want 2", n, err)
	}
	if len(pub.got) != 3 {
		t.Errorf("published = %v, want each event once", pub.got)
	}
}
