package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/payment/paymentdb"
	"github.com/funster-a/dd/internal/platform/events"
	"github.com/funster-a/dd/internal/platform/outbox"
)

// refundTickets возвращает стоимость билетов оплаченного заказа. После
// успеха у провайдера записывается order.refunded: booking освобождает
// места и меняет статус заказа — только после успешного возврата (spec.md).
func (s *Service) refundTickets(ctx context.Context, ev events.RefundRequestedEvent) error {
	if ev.RequestID == "" || ev.AmountTiyn <= 0 {
		return fmt.Errorf("refund tickets of order %s: request id and positive amount are required", ev.OrderID)
	}
	p, err := s.q.GetSucceededPayment(ctx, ev.OrderID)
	if err != nil {
		return fmt.Errorf("load payment of order %s: %w", ev.OrderID, err)
	}
	if p.ProviderPaymentID == nil {
		return fmt.Errorf("refund payment %s: no provider payment", p.ID)
	}

	var r paymentdb.Refund
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		r, err = q.InsertTicketRefund(ctx, paymentdb.InsertTicketRefundParams{
			OrganizerID: p.OrganizerID, PaymentID: p.ID, OrderID: p.OrderID,
			AmountTiyn: ev.AmountTiyn, Reason: ev.Reason, RequestID: &ev.RequestID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			r, err = q.GetRefundByRequest(ctx, &ev.RequestID)
			return err // возврат уже записан
		}
		if err != nil {
			return fmt.Errorf("save refund: %w", err)
		}
		// Сумма возвратов не больше суммы платежа (spec.md).
		other, err := q.SumOtherRefunds(ctx, paymentdb.SumOtherRefundsParams{PaymentID: p.ID, ID: r.ID})
		if err != nil {
			return fmt.Errorf("sum refunds: %w", err)
		}
		if other+r.AmountTiyn > p.AmountTiyn {
			return fmt.Errorf("refund %d tiyn of payment %s exceeds the rest (%d of %d refunded)",
				r.AmountTiyn, p.ID, other, p.AmountTiyn)
		}
		return q.InsertRefundItems(ctx, paymentdb.InsertRefundItemsParams{
			RefundID: r.ID, TicketIds: ev.TicketIDs, OrganizerID: p.OrganizerID, OrderID: p.OrderID,
		})
	})
	if err != nil {
		return err
	}
	if r.Status == "succeeded" {
		return nil
	}
	if err := s.q.SetRefundPending(ctx, r.ID); err != nil {
		return fmt.Errorf("mark refund pending: %w", err)
	}
	pr, err := s.gw.Refund(ctx, RefundRequest{RefundID: r.ID, ProviderPaymentID: *p.ProviderPaymentID, AmountTiyn: r.AmountTiyn})
	if errors.Is(err, ErrRejected) {
		if ferr := s.q.SetRefundFailed(ctx, r.ID); ferr != nil {
			s.log.ErrorContext(ctx, "mark refund failed", slog.Any("error", ferr))
		}
	}
	if err != nil {
		return fmt.Errorf("refund at provider: %w", err)
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := q.SetRefundSucceeded(ctx, paymentdb.SetRefundSucceededParams{ID: r.ID, ProviderRefundID: &pr.ID}); err != nil {
			return fmt.Errorf("mark refund succeeded: %w", err)
		}
		return outbox.Add(ctx, tx, events.OrderRefunded, events.OrderRefundedEvent{OrderID: p.OrderID, TicketIDs: ev.TicketIDs})
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "tickets refunded", slog.String("order_id", p.OrderID), slog.String("reason", ev.Reason),
		slog.Int("tickets", len(ev.TicketIDs)), slog.Int64("amount_tiyn", r.AmountTiyn))
	return nil
}
