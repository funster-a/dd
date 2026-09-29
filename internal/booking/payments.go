package booking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/booking/bookingdb"
	"github.com/funster-a/dd/internal/platform/events"
	"github.com/funster-a/dd/internal/platform/outbox"
)

// errSeatsLost — часть мест заказа после истечения холда купил другой заказ.
var errSeatsLost = errors.New("order seats were taken by another order")

// ConfirmPayment обрабатывает событие payment.succeeded. Заказ оплачен,
// если подтверждение пришло до конца холда и все места всё ещё за ним:
// места становятся sold, записывается событие order.paid. Иначе заказ не
// восстанавливается, а деньги возвращаются (spec.md, «Оплата после
// истечения удержания»). Повторная доставка события ничего не меняет.
func (s *Service) ConfirmPayment(ctx context.Context, ev events.PaymentSucceededEvent) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		o, err := q.LockOrder(ctx, ev.OrderID)
		if err != nil {
			return fmt.Errorf("lock order %s: %w", ev.OrderID, err)
		}
		switch {
		case o.Status == "paid":
			// Повтор события. Заказ оплачивает одна попытка: у заказа одна
			// действующая попытка оплаты и одна успешная (индексы в payments).
			return nil
		case o.Status != "pending":
			return s.requestRefund(ctx, tx, ev, o.Status)
		case !ev.PaidAt.Before(o.ExpiresAt) || ev.AmountTiyn != o.TotalTiyn:
			if ev.AmountTiyn != o.TotalTiyn {
				s.log.ErrorContext(ctx, "paid amount differs from order total", slog.String("order_id", o.ID),
					slog.Int64("paid", ev.AmountTiyn), slog.Int64("total", o.TotalTiyn))
			}
			if _, err := s.closeOrder(ctx, q, o.ID, o.ExpiresAt, ev.PaidAt, "cancelled"); err != nil {
				return err
			}
			return s.requestRefund(ctx, tx, ev, "late")
		}

		sold, err := q.SellOrderSeats(ctx, o.ID)
		if err != nil {
			return fmt.Errorf("sell seats: %w", err)
		}
		items, err := q.CountOrderItems(ctx, o.ID)
		if err != nil {
			return fmt.Errorf("count order items: %w", err)
		}
		if len(sold) != int(items) {
			return errSeatsLost // откат: продажа только целиком
		}
		if _, err := q.MarkOrderPaid(ctx, bookingdb.MarkOrderPaidParams{ID: o.ID, PaidAt: ev.PaidAt}); err != nil {
			return fmt.Errorf("mark order paid: %w", err)
		}
		return outbox.Add(ctx, tx, events.OrderPaid, events.OrderPaidEvent{OrderID: o.ID})
	})
	if !errors.Is(err, errSeatsLost) {
		return err
	}
	// Места ушли другим после истечения холда: заказ истёк, деньги назад.
	s.log.WarnContext(ctx, "payment arrived after seats were resold", slog.String("order_id", ev.OrderID))
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		o, err := q.LockOrder(ctx, ev.OrderID)
		if err != nil {
			return fmt.Errorf("lock order %s: %w", ev.OrderID, err)
		}
		if o.Status == "pending" {
			if _, err := s.closeOrder(ctx, q, o.ID, o.ExpiresAt, o.ExpiresAt, "expired"); err != nil {
				return err
			}
		}
		return s.requestRefund(ctx, tx, ev, "seats lost")
	})
}

func (s *Service) requestRefund(ctx context.Context, tx pgx.Tx, ev events.PaymentSucceededEvent, why string) error {
	s.log.InfoContext(ctx, "refund requested for payment", slog.String("payment_id", ev.PaymentID),
		slog.String("order_id", ev.OrderID), slog.String("why", why))
	return outbox.Add(ctx, tx, events.RefundRequested, events.RefundRequestedEvent{
		PaymentID: ev.PaymentID, OrderID: ev.OrderID, Reason: events.RefundLatePayment,
	})
}
