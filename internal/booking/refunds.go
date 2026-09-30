package booking

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/booking/bookingdb"
	"github.com/funster-a/dd/internal/platform/events"
)

// ApplyRefund обрабатывает order.refunded: места возвращённых билетов снова
// продаются, заказ становится partially_refunded или refunded — только
// после успешного возврата денег (spec.md, ADR 014). Повтор ничего не меняет.
func (s *Service) ApplyRefund(ctx context.Context, ev events.OrderRefundedEvent) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		o, err := q.LockOrder(ctx, ev.OrderID)
		if err != nil {
			return fmt.Errorf("lock order %s: %w", ev.OrderID, err)
		}
		if err := q.ReleaseRefundedSeats(ctx, ev.TicketIDs); err != nil {
			return fmt.Errorf("release seats: %w", err)
		}
		if o.Status != "paid" && o.Status != "partially_refunded" {
			return nil
		}
		left, err := q.CountUnrefundedTickets(ctx, o.ID)
		if err != nil {
			return fmt.Errorf("count tickets: %w", err)
		}
		status := "partially_refunded"
		if left == 0 {
			status = "refunded"
		}
		return q.SetOrderRefundStatus(ctx, bookingdb.SetOrderRefundStatusParams{ID: o.ID, Status: status})
	})
}
