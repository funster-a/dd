package ticket

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/funster-a/dd/internal/platform/events"
)

func (e *env) outbox(t *testing.T, topic string) []string {
	t.Helper()
	rows, err := e.db.Pool.Query(context.Background(), `SELECT payload::text FROM outbox WHERE topic = $1 ORDER BY id`, topic)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func refundCode(err error) string {
	var pe *PreconditionError
	var ve *ValidationError
	switch {
	case errors.As(err, &pe):
		return pe.Code
	case errors.As(err, &ve):
		return "invalid_" + ve.Field
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case err == nil:
		return "ok"
	}
	return err.Error()
}

func TestRequestRefundRules(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	order, buyer := e.order(t, "paid", e.seats[0], e.seats[1])
	if err := e.svc.Issue(ctx, events.OrderPaidEvent{OrderID: order}); err != nil {
		t.Fatal(err)
	}
	ts, err := e.svc.ForOrder(ctx, buyer, order)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	refund := func(buyerID string, ids ...string) error {
		_, err := e.svc.RequestRefund(ctx, buyerID, order, ids, now)
		return err
	}

	if got := refundCode(refund(uuid.NewString(), ts[0].ID)); got != "not_found" {
		t.Errorf("someone else's order = %s", got)
	}
	if got := refundCode(refund(buyer, uuid.NewString())); got != "not_found" {
		t.Errorf("ticket from another order = %s", got)
	}
	if got := refundCode(refund(buyer)); got != "invalid_ticket_ids" {
		t.Errorf("no tickets = %s", got)
	}
	if got := refundCode(refund(buyer, ts[0].ID, ts[0].ID)); got != "invalid_ticket_ids" {
		t.Errorf("repeated ticket = %s", got)
	}

	// Использованный билет не возвращается.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE tickets SET status = 'used', used_at = now() WHERE id = $1`, ts[1].ID); err != nil {
		t.Fatal(err)
	}
	if got := refundCode(refund(buyer, ts[0].ID, ts[1].ID)); got != "ticket_used" {
		t.Errorf("used ticket = %s", got)
	}
	// Отказ не аннулировал ни один билет: возврат только целиком.
	if v, _ := e.svc.ByToken(ctx, tokenOf(ts[0].URL)); v.Status != "issued" {
		t.Errorf("ticket after a rejected refund = %s", v.Status)
	}

	req, err := e.svc.RequestRefund(ctx, buyer, order, []string{ts[0].ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	// Покупатель вернул билет сам: возвращается цена, сервисный сбор — нет
	// (ADR 019).
	if req.AmountTiyn != 500000 || req.Status != "requested" {
		t.Errorf("request = %+v, want 500000 without the service fee", req)
	}
	if v, _ := e.svc.ByToken(ctx, tokenOf(ts[0].URL)); v.Status != "revoked" {
		t.Errorf("refunded ticket = %s, want revoked at once", v.Status)
	}
	if got := len(e.outbox(t, events.RefundRequested)); got != 1 {
		t.Errorf("refund.requested events = %d", got)
	}
	if got := refundCode(refund(buyer, ts[0].ID)); got != "ticket_already_refunded" {
		t.Errorf("second refund = %s", got)
	}

	// Срок возврата: не позже чем за refund_deadline_hours до начала.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE events SET starts_at = now() + interval '2 hours', ends_at = now() + interval '4 hours' WHERE id = $1`, e.event); err != nil {
		t.Fatal(err)
	}
	other, otherBuyer := e.order(t, "paid", e.seats[2])
	if err := e.svc.Issue(ctx, events.OrderPaidEvent{OrderID: other}); err != nil {
		t.Fatal(err)
	}
	ots, _ := e.svc.ForOrder(ctx, otherBuyer, other)
	if _, err := e.svc.RequestRefund(ctx, otherBuyer, other, []string{ots[0].ID}, now); refundCode(err) != "refund_deadline_passed" {
		t.Errorf("after the deadline = %s", refundCode(err))
	}
}

// Бесплатные билеты возвращаются без денег: сразу освобождаются места.
func TestRefundFreeTickets(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	order, buyer := e.order(t, "paid", e.seats[0])
	if _, err := e.db.Pool.Exec(ctx, `UPDATE order_items SET price_tiyn = 0, fee_tiyn = 0 WHERE order_id = $1`, order); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Issue(ctx, events.OrderPaidEvent{OrderID: order}); err != nil {
		t.Fatal(err)
	}
	ts, _ := e.svc.ForOrder(ctx, buyer, order)
	if _, err := e.svc.RequestRefund(ctx, buyer, order, []string{ts[0].ID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(e.outbox(t, events.RefundRequested)) != 0 || len(e.outbox(t, events.OrderRefunded)) != 1 {
		t.Error("free ticket refund must skip payment and release the seat")
	}
}

// Повторная доставка event.cancelled не создаёт второй запрос возврата с
// другим ключом: ключ заказа детерминирован.
func TestEventCancelledIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	o1, _ := e.order(t, "paid", e.seats[0], e.seats[1])
	o2, _ := e.order(t, "paid", e.seats[2])
	for _, o := range []string{o1, o2} {
		if err := e.svc.Issue(ctx, events.OrderPaidEvent{OrderID: o}); err != nil {
			t.Fatal(err)
		}
	}
	ev := events.EventCancelledEvent{EventID: e.event}
	if err := e.svc.HandleEventCancelled(ctx, ev); err != nil {
		t.Fatal(err)
	}
	first := e.outbox(t, events.RefundRequested)
	if len(first) != 2 {
		t.Fatalf("refund requests = %d, want one per order", len(first))
	}
	// Событие отменил организатор: покупатель получает всё, что заплатил,
	// вместе с сервисным сбором (ADR 019).
	amounts := map[string]int64{}
	for _, raw := range first {
		var ev events.RefundRequestedEvent
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			t.Fatal(err)
		}
		amounts[ev.OrderID] = ev.AmountTiyn
	}
	if amounts[o1] != 2*(500000+testFee) || amounts[o2] != 500000+testFee {
		t.Errorf("cancellation refunds = %v, want price and service fee of every ticket", amounts)
	}
	var active int
	if err := e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE event_id = $1 AND status = 'issued'`, e.event).Scan(&active); err != nil || active != 0 {
		t.Errorf("issued tickets after cancellation = %d", active)
	}
	// Все билеты аннулированы — повтор ничего не находит и ничего не пишет.
	if err := e.svc.HandleEventCancelled(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if again := e.outbox(t, events.RefundRequested); len(again) != 2 {
		t.Errorf("refund requests after redelivery = %d, want 2", len(again))
	}
}
