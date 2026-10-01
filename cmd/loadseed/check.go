package main

import (
	"context"
	"errors"
	"flag"

	"github.com/jackc/pgx/v5/pgxpool"
)

// check проверяет главный инвариант после прогона на уровне данных: ни одно
// место не попало в два активных заказа, каждая позиция активного заказа
// держится именно этим заказом, число занятых мест равно числу позиций.
func check(ctx context.Context, pool *pgxpool.Pool, args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	event := fs.String("event", "", "id события")
	out := fs.String("out", "-", "куда записать итог")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var r struct {
		ActiveOrders int `json:"active_orders"`
		OrderItems   int `json:"order_items"`
		HeldOrSold   int `json:"seats_held_or_sold"`
		DoubleBooked int `json:"double_booked"`
		Mismatched   int `json:"mismatched"`
	}
	err := pool.QueryRow(ctx, `
		WITH active AS (SELECT id FROM orders WHERE event_id = $1 AND status IN ('pending', 'paid')),
		items AS (SELECT i.* FROM order_items i JOIN active a ON a.id = i.order_id)
		SELECT
		  (SELECT count(*) FROM active),
		  (SELECT count(*) FROM items),
		  (SELECT count(*) FROM event_seats WHERE event_id = $1 AND status IN ('held', 'sold')),
		  (SELECT count(*) FROM (SELECT event_seat_id FROM items GROUP BY 1 HAVING count(*) > 1) d),
		  (SELECT count(*) FROM items i JOIN event_seats s ON s.id = i.event_seat_id
		     WHERE s.status = 'held' AND s.hold_order_id <> i.order_id)`, *event).
		Scan(&r.ActiveOrders, &r.OrderItems, &r.HeldOrSold, &r.DoubleBooked, &r.Mismatched)
	if err != nil {
		return err
	}
	if err := writeJSON(*out, r); err != nil {
		return err
	}
	if r.DoubleBooked > 0 || r.Mismatched > 0 || r.HeldOrSold != r.OrderItems {
		return errors.New("invariant violated: a seat is booked more than once or holds disagree with orders")
	}
	return nil
}
