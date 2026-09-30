package ticket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/platform/events"
	"github.com/funster-a/dd/internal/platform/outbox"
	"github.com/funster-a/dd/internal/ticket/ticketdb"
)

// PreconditionError — возврат невозможен в текущем состоянии. HTTP 422.
type PreconditionError struct {
	Code    string
	Message string
}

func (e *PreconditionError) Error() string { return e.Message }

// RefundRequest — принятый запрос возврата билетов.
type RefundRequest struct {
	ID         string   `json:"id"`
	OrderID    string   `json:"order_id"`
	TicketIDs  []string `json:"ticket_ids"`
	AmountTiyn int64    `json:"amount_tiyn"`
	Status     string   `json:"status"` // requested: деньги вернёт платёжный модуль
}

// maxRefundTickets — сколько билетов можно вернуть одним запросом.
const maxRefundTickets = 50

// RequestRefund принимает возврат билетов заказа покупателя (ADR 014).
// Билеты аннулируются сразу, в той же транзакции: по ним уже нельзя пройти,
// пока деньги возвращаются. Деньги возвращает payment по событию
// refund.requested; бесплатные билеты просто освобождают места.
func (s *Service) RequestRefund(ctx context.Context, buyerID, orderID string, ticketIDs []string, now time.Time) (RefundRequest, error) {
	if uuid.Validate(orderID) != nil {
		return RefundRequest{}, ErrNotFound
	}
	if len(ticketIDs) == 0 || len(ticketIDs) > maxRefundTickets {
		return RefundRequest{}, &ValidationError{Field: "ticket_ids", Message: fmt.Sprintf("choose 1-%d tickets", maxRefundTickets)}
	}
	ids := slices.Clone(ticketIDs)
	slices.Sort(ids)
	if len(slices.Compact(ids)) != len(ticketIDs) {
		return RefundRequest{}, &ValidationError{Field: "ticket_ids", Message: "tickets must not repeat"}
	}
	for _, id := range ids {
		if uuid.Validate(id) != nil {
			return RefundRequest{}, &ValidationError{Field: "ticket_ids", Message: "must be ticket ids"}
		}
	}

	rc, err := s.q.GetRefundContext(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && rc.BuyerID != buyerID) {
		return RefundRequest{}, ErrNotFound
	}
	if err != nil {
		return RefundRequest{}, fmt.Errorf("load order: %w", err)
	}
	switch {
	case rc.EventStatus == "cancelled":
		return RefundRequest{}, &PreconditionError{Code: "event_cancelled", Message: "event is cancelled, all tickets are refunded automatically"}
	case rc.Status != "paid" && rc.Status != "partially_refunded":
		return RefundRequest{}, &PreconditionError{Code: "order_not_refundable", Message: "order is " + rc.Status}
	}
	deadline := rc.StartsAt.Add(-time.Duration(rc.RefundDeadlineHours) * time.Hour)
	if !now.Before(deadline) {
		return RefundRequest{}, &PreconditionError{
			Code:    "refund_deadline_passed",
			Message: fmt.Sprintf("tickets can be returned until %s UTC", deadline.UTC().Format("2006-01-02 15:04")),
		}
	}

	req := RefundRequest{ID: uuid.Must(uuid.NewV7()).String(), OrderID: orderID, TicketIDs: ids, Status: "requested"}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		rows, err := q.LockOrderTickets(ctx, ticketdb.LockOrderTicketsParams{OrderID: orderID, Ids: ids})
		if err != nil {
			return fmt.Errorf("lock tickets: %w", err)
		}
		if len(rows) != len(ids) {
			return ErrNotFound // билет не из этого заказа
		}
		for _, r := range rows {
			switch r.Status {
			case "used":
				return &PreconditionError{Code: "ticket_used", Message: "a used ticket cannot be returned"}
			case "revoked":
				return &PreconditionError{Code: "ticket_already_refunded", Message: "ticket is already returned"}
			}
			req.AmountTiyn += r.PriceTiyn
		}
		if err := q.RevokeTickets(ctx, ids); err != nil {
			return fmt.Errorf("revoke tickets: %w", err)
		}
		return emitRefund(ctx, tx, req.ID, orderID, events.RefundBuyerRequest, ids, req.AmountTiyn)
	})
	if err != nil {
		return RefundRequest{}, err
	}
	s.log.InfoContext(ctx, "refund requested", slog.String("order_id", orderID), slog.Int("tickets", len(ids)),
		slog.Int64("amount_tiyn", req.AmountTiyn))
	return req, nil
}

// emitRefund записывает событие возврата: деньги возвращает payment, а за
// бесплатные билеты возвращать нечего — места сразу освобождает booking.
func emitRefund(ctx context.Context, tx pgx.Tx, requestID, orderID, reason string, ticketIDs []string, amount int64) error {
	if amount == 0 {
		return outbox.Add(ctx, tx, events.OrderRefunded, events.OrderRefundedEvent{OrderID: orderID, TicketIDs: ticketIDs})
	}
	return outbox.Add(ctx, tx, events.RefundRequested, events.RefundRequestedEvent{
		RequestID: requestID, OrderID: orderID, Reason: reason, TicketIDs: ticketIDs, AmountTiyn: amount,
	})
}

// cancelNamespace — пространство имён UUIDv5 для ключей возврата при отмене
// события: ключ одного заказа всегда один и тот же, поэтому повторная
// доставка event.cancelled не вернёт деньги дважды.
var cancelNamespace = uuid.MustParse("6f1c2a5e-8b7d-4e0a-9c3f-2d1b0a4e5f60")

// HandleEventCancelled аннулирует билеты отменённого события и запрашивает
// полный возврат по каждому заказу (обработчик события event.cancelled).
func (s *Service) HandleEventCancelled(ctx context.Context, ev events.EventCancelledEvent) error {
	var orders int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		rows, err := q.LockEventTicketsForCancel(ctx, ev.EventID)
		if err != nil {
			return fmt.Errorf("lock event tickets: %w", err)
		}
		var issued []string
		for i := 0; i < len(rows); {
			orderID := rows[i].OrderID
			var ids []string
			var amount int64
			for ; i < len(rows) && rows[i].OrderID == orderID; i++ {
				ids = append(ids, rows[i].ID)
				amount += rows[i].PriceTiyn
				if rows[i].Status == "issued" {
					issued = append(issued, rows[i].ID)
				}
			}
			key := uuid.NewSHA1(cancelNamespace, []byte(ev.EventID+"/"+orderID)).String()
			if err := emitRefund(ctx, tx, key, orderID, events.RefundEventCancelled, ids, amount); err != nil {
				return err
			}
			orders++
		}
		if len(issued) == 0 {
			return nil
		}
		return q.RevokeTickets(ctx, issued)
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "event cancelled: tickets revoked, refunds requested",
		slog.String("event_id", ev.EventID), slog.Int("orders", orders))
	return nil
}
