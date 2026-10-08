package main

import (
	"context"
	"errors"
	"flag"
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
	// DoubleBooked — места, которые входят больше чем в одну действующую позицию.
	DoubleBooked int `json:"double_booked"`
	// Mismatched — действующие позиции, чьё место не в том состоянии: у
	// корзины место должно держаться этим заказом, у оплаченного — быть продано.
	Mismatched int `json:"mismatched"`
	// Orphaned — занятые места без своей позиции: холд без позиции этого
	// заказа или проданное место без позиции оплаченного заказа.
	Orphaned int `json:"orphaned"`
}

func (r invariant) violated() bool {
	return r.DoubleBooked > 0 || r.Mismatched > 0 || r.Orphaned > 0 || r.HeldOrSold != r.OrderItems
}

// rowQuerier — пул или транзакция: тесты проверяют подложенные ошибки в
// транзакции и откатывают её.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// checkInvariant сверяет места события с заказами на момент at.
//
// Действующая позиция — та же, что в лимитах билетов (CountBuyerTickets):
// позиция оплаченного или частично возвращённого заказа без аннулированного
// билета либо позиция корзины, чей срок ещё не вышел. Занятое место — проданное
// или с действующим холдом. Корзина после конца срока ничего не держит, даже
// если воркер ещё не перевёл её в expired: её место законно может взять другой.
//
// Инвариант: занятые места и действующие позиции соответствуют один к одному.
// Ни одно место не входит в две позиции, каждая позиция держит своё место в
// нужном состоянии, и у каждого занятого места есть своя позиция. Проданное
// место сверяется с позицией оплаченного заказа по отдельности, а не только
// общим счётом: иначе проданное без оплаты место и потерянное место
// оплаченного заказа взаимно гасились бы.
//
// Возврат проходит в два шага (билет аннулирован, затем место освобождено),
// поэтому проверять нужно, когда очередь событий разобрана: проданное место
// возвращённого билета на этом промежутке видно как место без позиции.
func checkInvariant(ctx context.Context, q rowQuerier, eventID string, at time.Time) (invariant, error) {
	var r invariant
	err := q.QueryRow(ctx, `
		WITH active AS (
		  SELECT id, status FROM orders
		  WHERE event_id = $1
		    AND (status IN ('paid', 'partially_refunded') OR (status = 'pending' AND expires_at > $2))
		),
		items AS (
		  SELECT i.order_id, i.event_seat_id, a.status = 'pending' AS cart
		  FROM order_items i
		  JOIN active a ON a.id = i.order_id
		  LEFT JOIN tickets t ON t.order_item_id = i.id
		  WHERE t.id IS NULL OR t.status <> 'revoked'
		),
		taken AS (
		  SELECT id, status, hold_order_id FROM event_seats
		  WHERE event_id = $1 AND (status = 'sold' OR (status = 'held' AND hold_expires_at > $2))
		)
		SELECT
		  (SELECT count(*) FROM active),
		  (SELECT count(*) FROM items),
		  (SELECT count(*) FROM taken),
		  (SELECT count(*) FROM (SELECT event_seat_id FROM items GROUP BY 1 HAVING count(*) > 1) d),
		  (SELECT count(*) FROM items i LEFT JOIN taken s ON s.id = i.event_seat_id
		     WHERE s.id IS NULL OR NOT CASE
		       WHEN i.cart THEN s.status = 'held' AND s.hold_order_id = i.order_id
		       ELSE s.status = 'sold' END),
		  (SELECT count(*) FROM taken s WHERE NOT EXISTS (
		     SELECT 1 FROM items i WHERE i.event_seat_id = s.id AND CASE
		       WHEN s.status = 'held' THEN i.cart AND i.order_id = s.hold_order_id
		       ELSE NOT i.cart END))`, eventID, at).
		Scan(&r.ActiveOrders, &r.OrderItems, &r.HeldOrSold, &r.DoubleBooked, &r.Mismatched, &r.Orphaned)
	return r, err
}
