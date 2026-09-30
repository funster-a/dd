package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/catalog/catalogdb"
	"github.com/funster-a/dd/internal/platform/events"
	"github.com/funster-a/dd/internal/platform/outbox"
)

// CancelEvent отменяет событие. У опубликованного события в той же
// транзакции записывается event.cancelled: билеты аннулируются, а деньги за
// них возвращаются полностью (spec.md, ADR 014). Черновик просто закрывается.
func (s *Service) CancelEvent(ctx context.Context, organizerID, id string) (Event, error) {
	var out Event
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		cur, err := q.GetEventForUpdate(ctx, catalogdb.GetEventForUpdateParams{OrganizerID: organizerID, ID: id})
		if err != nil {
			return notFound(err, "load event")
		}
		if cur.Status == "cancelled" {
			return &ConflictError{Code: "event_cancelled", Message: "event is already cancelled"}
		}
		e, err := q.CancelEvent(ctx, catalogdb.CancelEventParams{OrganizerID: organizerID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return &ConflictError{Code: "event_cancelled", Message: "event is already cancelled"}
		}
		if err != nil {
			return fmt.Errorf("cancel event: %w", err)
		}
		if cur.Status == "published" {
			if err := outbox.Add(ctx, tx, events.EventCancelled, events.EventCancelledEvent{EventID: id}); err != nil {
				return err
			}
		}
		out = eventFrom(e)
		return nil
	})
	if err != nil {
		return Event{}, err
	}
	s.invalidatePublic(ctx, organizerID, out.Slug)
	return out, nil
}
