package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// check проверяет главный инвариант после прогона на уровне данных (см.
// checkInvariant) и завершается ошибкой, если он нарушен.
func check(ctx context.Context, pool *pgxpool.Pool, args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	event := fs.String("event", "", "id события")
	out := fs.String("out", "-", "куда записать итог")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := checkInvariant(ctx, pool, *event, time.Now())
	if err != nil {
		return err
	}
	if err := writeJSON(*out, r); err != nil {
		return err
	}
	if r.violated() {
		return errors.New("invariant violated: a seat is booked more than once or seats disagree with orders")
	}
	return nil
}

// invariant — итог проверки одного события.
type invariant struct {
	ActiveOrders int `json:"active_orders"`
	OrderItems   int `json:"order_items"`
	HeldOrSold   int `json:"seats_held_or_sold"`
	// DoubleBooked — места, на которые больше одной действующей позиции.
	DoubleBooked int `json:"double_booked"`
	// Mismatched — действующие позиции, чьё место не в том состоянии: у
	// корзины место должно держаться этим заказом, у оплаченного — быть продано.
	Mismatched int `json:"mismatched"`
	// Orphaned — занятые места без своей позиции: холд без позиции этого
	// заказа или проданное место без позиции оплаченного заказа.
	Orphaned int `json:"orphaned"`
	// EventCancelled — событие отменено: продаж больше нет, сверяются только
	// двойные брони (см. checkInvariant).
	EventCancelled bool `json:"event_cancelled,omitempty"`
}

func (r invariant) violated() bool {
	if r.DoubleBooked > 0 {
		return true
	}
	return !r.EventCancelled && (r.Mismatched > 0 || r.Orphaned > 0 || r.HeldOrSold != r.OrderItems)
}

// rowQuerier — пул или транзакция: тесты проверяют подложенные ошибки в
// транзакции и откатывают её.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// checkInvariant сверяет места события с заказами на момент at.
//
// Действующая позиция держит место законно:
//   - позиция корзины, чей срок ещё не вышел. Корзина после конца срока ничего
//     не держит, даже если воркер ещё не перевёл её в expired: её место
//     законно может взять другой;
//   - позиция оплаченного заказа, пока деньги за неё не вернулись. Условие то
//     же, что у CountUnrefundedTickets: место освобождается только после
//     успешного возврата (ADR 014), поэтому аннулированный билет с
//     незавершённым или отклонённым возвратом место по-прежнему держит.
//
// Занятое место — проданное или с действующим холдом.
//
// Инвариант: занятые места и действующие позиции соответствуют один к одному.
// Ни одно место не входит в две позиции, каждая позиция держит своё место в
// нужном состоянии, и у каждого занятого места есть своя позиция. Проданное
// место сверяется с позицией оплаченного заказа по отдельности, а не только
// общим счётом: иначе проданное без оплаты место и потерянное место
// оплаченного заказа взаимно гасились бы.
//
// Проверять нужно, когда очередь событий разобрана: место успешно
// возвращённого билета освобождает следующее событие, order.refunded, и до
// него видно как место без позиции. У отменённого события сверяются только
// двойные брони: продаж больше нет, а при отмене освобождаются и места
// билетов, по которым уже прошли (HandleEventCancelled, ApplyRefund).
func checkInvariant(ctx context.Context, q rowQuerier, eventID string, at time.Time) (invariant, error) {
	var r invariant
	var cancelled *bool
	err := q.QueryRow(ctx, `
		WITH claims AS (
		  SELECT i.order_id, i.event_seat_id, o.status = 'pending' AS cart
		  FROM order_items i
		  JOIN orders o ON o.id = i.order_id
		  LEFT JOIN tickets t ON t.order_item_id = i.id
		  WHERE o.event_id = $1 AND (
		    (o.status = 'pending' AND o.expires_at > $2)
		    OR (o.status IN ('paid', 'partially_refunded', 'refunded') AND (t.id IS NULL OR NOT (
		      (t.status = 'revoked' AND i.price_tiyn = 0)
		      OR EXISTS (SELECT 1 FROM refund_items ri JOIN refunds f ON f.id = ri.refund_id
		                 WHERE ri.ticket_id = t.id AND f.status = 'succeeded'))))
		  )
		),
		taken AS (
		  SELECT id, status, hold_order_id FROM event_seats
		  WHERE event_id = $1 AND (status = 'sold' OR (status = 'held' AND hold_expires_at > $2))
		)
		SELECT
		  (SELECT count(DISTINCT order_id) FROM claims),
		  (SELECT count(*) FROM claims),
		  (SELECT count(*) FROM taken),
		  (SELECT count(*) FROM (SELECT event_seat_id FROM claims GROUP BY 1 HAVING count(*) > 1) d),
		  (SELECT count(*) FROM claims c LEFT JOIN taken s ON s.id = c.event_seat_id
		     WHERE s.id IS NULL OR NOT CASE
		       WHEN c.cart THEN s.status = 'held' AND s.hold_order_id = c.order_id
		       ELSE s.status = 'sold' END),
		  (SELECT count(*) FROM taken s WHERE NOT EXISTS (
		     SELECT 1 FROM claims c WHERE c.event_seat_id = s.id AND CASE
		       WHEN s.status = 'held' THEN c.cart AND c.order_id = s.hold_order_id
		       ELSE NOT c.cart END)),
		  (SELECT status = 'cancelled' FROM events WHERE id = $1)`, eventID, at).
		Scan(&r.ActiveOrders, &r.OrderItems, &r.HeldOrSold, &r.DoubleBooked, &r.Mismatched, &r.Orphaned, &cancelled)
	if err != nil {
		return r, err
	}
	// Без этого опечатка в id события давала бы чистый итог из одних нулей.
	if cancelled == nil {
		return r, fmt.Errorf("event %s not found", eventID)
	}
	r.EventCancelled = *cancelled
	return r, nil
}
