package catalog

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCancelEvent(t *testing.T) {
	e, cache, orgSlug := newPublicEnv(t)
	ctx := t.Context()
	ev := e.draft(t)
	e.ready(t, ev)
	if _, err := e.svc.Publish(ctx, e.org, ev.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.GetPublicEvent(ctx, orgSlug, ev.Slug); err != nil { // страница в кэше
		t.Fatal(err)
	}

	got, err := e.svc.CancelEvent(ctx, e.org, ev.ID)
	if err != nil || got.Status != "cancelled" {
		t.Fatalf("CancelEvent = %+v, %v", got, err)
	}
	var n int
	if err := e.svc.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic = 'event.cancelled' AND payload->>'event_id' = $1`, ev.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("event.cancelled events = %d, %v; want 1", n, err)
	}
	// Покупатель по старой ссылке видит, что событие отменено: кэш сброшен.
	if len(cache.data) != 0 {
		t.Error("public page cache survived cancellation")
	}
	b, err := e.svc.GetPublicEvent(ctx, orgSlug, ev.Slug)
	if err != nil {
		t.Fatal(err)
	}
	var pe PublicEvent
	if err := json.Unmarshal(b, &pe); err != nil || pe.Status != "cancelled" {
		t.Errorf("public status = %q, %v; want cancelled", pe.Status, err)
	}
	if _, err := e.svc.CancelEvent(ctx, e.org, ev.ID); !isConflict(err, "event_cancelled") {
		t.Errorf("second cancel: err = %v, want event_cancelled", err)
	}

	// Черновик закрывается без событий для возвратов: билетов у него нет.
	d := e.draft(t)
	if _, err := e.svc.CancelEvent(ctx, e.org, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE payload->>'event_id' = $1`, d.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("draft cancel emitted %d events", n)
	}
	if _, err := e.svc.CancelEvent(ctx, "00000000-0000-7000-8000-000000000000", ev.ID); err == nil {
		t.Error("another organizer cancelled the event")
	}
}
