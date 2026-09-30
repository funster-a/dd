package booking

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/funster-a/dd/internal/platform/events"
)

func paid(o Order, at time.Time) events.PaymentSucceededEvent {
	return events.PaymentSucceededEvent{PaymentID: uuid.NewString(), OrderID: o.ID, AmountTiyn: o.TotalTiyn, PaidAt: at}
}

// events — сколько событий topic записано для заказа.
func (e *env) events(t *testing.T, topic, orderID string) int {
	t.Helper()
	var n int
	if err := e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM outbox WHERE topic = $1 AND payload->>'order_id' = $2`, topic, orderID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) orderStatus(t *testing.T, id string) string {
	t.Helper()
	var s string
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT status FROM orders WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConfirmPaymentInTime(t *testing.T) {
	e := newEnv(t, false, 1, 3, 10)
	ctx := t.Context()
	req := seatsReq(seat(1, 1), seat(1, 2))
	req.General = []GeneralRef{{Section: "Фан-зона", Quantity: 2}}
	o, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ev := paid(o, time.Now())
	if err := e.svc.ConfirmPayment(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if s := e.orderStatus(t, o.ID); s != "paid" {
		t.Fatalf("order = %s, want paid", s)
	}
	if st, holder := e.seatStatus(t, seat(1, 1)); st != "sold" || holder != nil {
		t.Errorf("seat 1-1 = %s held by %v, want sold", st, holder)
	}
	if e.events(t, events.OrderPaid, o.ID) != 1 || e.events(t, events.RefundRequested, o.ID) != 0 {
		t.Error("want one order.paid and no refund")
	}
	a := e.availability(t)
	if len(a.Taken) != 2 || a.General[0].Available != 8 {
		t.Errorf("availability = %+v", a)
	}
	// Повторная доставка события ничего не меняет.
	if err := e.svc.ConfirmPayment(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if n := e.events(t, events.OrderPaid, o.ID); n != 1 {
		t.Errorf("order.paid events = %d after redelivery, want 1", n)
	}
	// Проданное место не купить, в том числе после «истечения» холда.
	if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now().Add(time.Hour)); err == nil {
		t.Error("sold seat was booked again")
	}
	if _, err := e.svc.CancelOrder(ctx, "", o.ID, time.Now()); err == nil {
		t.Error("cancelled a paid order")
	}
}

// Решает момент подтверждения оплаты, а не момент обработки события: очередь
// могла задержать событие дольше холда.
func TestConfirmPaymentProcessedAfterHold(t *testing.T) {
	e := newEnv(t, false, 1, 2, 0)
	created := time.Now().Add(-HoldTTL - time.Minute)
	o, err := e.svc.CreateOrder(t.Context(), e.buyer(t), e.eventID, seatsReq(seat(1, 1)), created)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConfirmPayment(t.Context(), paid(o, created.Add(5*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if s := e.orderStatus(t, o.ID); s != "paid" {
		t.Errorf("order = %s, want paid", s)
	}
}

// Оплата после истечения холда: заказ не восстанавливается, деньги назад.
func TestConfirmPaymentLate(t *testing.T) {
	e := newEnv(t, false, 1, 2, 0)
	created := time.Now().Add(-HoldTTL - time.Minute)
	o, err := e.svc.CreateOrder(t.Context(), e.buyer(t), e.eventID, seatsReq(seat(1, 1)), created)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConfirmPayment(t.Context(), paid(o, time.Now())); err != nil {
		t.Fatal(err)
	}
	if s := e.orderStatus(t, o.ID); s != "expired" {
		t.Errorf("order = %s, want expired", s)
	}
	if st, _ := e.seatStatus(t, seat(1, 1)); st != "available" {
		t.Errorf("seat = %s, want available", st)
	}
	if e.events(t, events.RefundRequested, o.ID) != 1 || e.events(t, events.OrderPaid, o.ID) != 0 {
		t.Error("want a refund request and no order.paid")
	}
}

// Оплата успела вовремя, но место после истечения холда уже купил другой:
// продажа только целиком, поэтому заказ истекает, деньги назад.
func TestConfirmPaymentSeatsLost(t *testing.T) {
	e := newEnv(t, false, 1, 2, 0)
	ctx := t.Context()
	created := time.Now().Add(-HoldTTL - time.Minute)
	o, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1), seat(1, 2)), created)
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 2)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConfirmPayment(ctx, paid(o, created.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if s := e.orderStatus(t, o.ID); s != "expired" {
		t.Errorf("order = %s, want expired", s)
	}
	if e.events(t, events.RefundRequested, o.ID) != 1 {
		t.Error("want a refund request")
	}
	if st, _ := e.seatStatus(t, seat(1, 1)); st != "available" {
		t.Errorf("seat 1-1 = %s, want available (not partially sold)", st)
	}
	if st, holder := e.seatStatus(t, seat(1, 2)); st != "held" || *holder != other.ID {
		t.Errorf("seat 1-2 = %s held by %v, want the other order", st, holder)
	}
	e.checkNoDoubleBooking(t)
}

func TestConfirmPaymentForClosedOrder(t *testing.T) {
	e := newEnv(t, false, 1, 2, 0)
	ctx := t.Context()
	buyer := e.buyer(t)
	o, err := e.svc.CreateOrder(ctx, buyer, e.eventID, seatsReq(seat(1, 1)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CancelOrder(ctx, buyer, o.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConfirmPayment(ctx, paid(o, time.Now())); err != nil {
		t.Fatal(err)
	}
	if s := e.orderStatus(t, o.ID); s != "cancelled" || e.events(t, events.RefundRequested, o.ID) != 1 {
		t.Errorf("order = %s, refunds = %d; want cancelled with a refund", s, e.events(t, events.RefundRequested, o.ID))
	}

	o2, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 2)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wrong := paid(o2, time.Now())
	wrong.AmountTiyn--
	if err := e.svc.ConfirmPayment(ctx, wrong); err != nil {
		t.Fatal(err)
	}
	if s := e.orderStatus(t, o2.ID); s == "paid" || e.events(t, events.RefundRequested, o2.ID) != 1 {
		t.Errorf("wrong amount: order = %s, want not paid with a refund", s)
	}
}

// Подтверждение оплаты и фоновое истечение гоняются за один заказ: исход
// ровно один — либо оплачен и места проданы, либо истёк и деньги назад.
func TestConfirmPaymentRacesExpiry(t *testing.T) {
	e := newEnv(t, false, 1, 20, 0)
	ctx := t.Context()
	created := time.Now().Add(-HoldTTL - time.Minute)
	orders := make([]Order, 20)
	for i := range orders {
		o, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, i+1)), created)
		if err != nil {
			t.Fatal(err)
		}
		orders[i] = o
	}
	var wg sync.WaitGroup
	for _, o := range orders {
		wg.Go(func() {
			if err := e.svc.ConfirmPayment(ctx, paid(o, created.Add(time.Minute))); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Go(func() {
		for {
			n, err := e.svc.ExpireOrders(ctx, time.Now(), 3)
			if err != nil {
				t.Error(err)
				return
			}
			if n == 0 {
				return
			}
		}
	})
	wg.Wait()
	var paidN, expiredN int
	for i, o := range orders {
		st, _ := e.seatStatus(t, seat(1, i+1))
		paidEv, refundEv := e.events(t, events.OrderPaid, o.ID), e.events(t, events.RefundRequested, o.ID)
		switch e.orderStatus(t, o.ID) {
		case "paid":
			paidN++
			if st != "sold" || paidEv != 1 || refundEv != 0 {
				t.Errorf("paid order %d: seat %s, paid events %d, refunds %d", i, st, paidEv, refundEv)
			}
		case "expired":
			expiredN++
			if st != "available" || paidEv != 0 || refundEv != 1 {
				t.Errorf("expired order %d: seat %s, paid events %d, refunds %d", i, st, paidEv, refundEv)
			}
		default:
			t.Errorf("order %d = %s", i, e.orderStatus(t, o.ID))
		}
	}
	t.Logf("paid %d, expired with refund %d", paidN, expiredN)
}

// Лимит билетов — на покупателя за всё событие: уже оплаченные билеты
// уменьшают, сколько можно взять новым заказом (лимит в фикстуре — 10).
func TestBuyerLimitAcrossOrders(t *testing.T) {
	e := newEnv(t, false, 0, 0, 20)
	ctx := t.Context()
	buyer := e.buyer(t)
	o, err := e.svc.CreateOrder(ctx, buyer, e.eventID, generalReq(6), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConfirmPayment(ctx, paid(o, time.Now())); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreateOrder(ctx, buyer, e.eventID, generalReq(5), time.Now())
	if pe, ok := err.(*PreconditionError); !ok || pe.Code != "ticket_limit_exceeded" { //nolint:errorlint // ошибка не оборачивается
		t.Fatalf("second order over the limit = %v, want ticket_limit_exceeded", err)
	}
	if _, err := e.svc.CreateOrder(ctx, buyer, e.eventID, generalReq(4), time.Now()); err != nil {
		t.Errorf("order within the remaining limit: %v", err)
	}
	// Другой покупатель лимит не делит.
	if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, generalReq(10), time.Now()); err != nil {
		t.Errorf("another buyer: %v", err)
	}
}

// Бесплатные билеты: заказ оформляется сразу, без оплаты.
func TestFreeOrder(t *testing.T) {
	e := newEnv(t, false, 1, 2, 5)
	ctx := t.Context()
	e.exec(t, `UPDATE price_categories SET price_tiyn = 0 WHERE event_id = $1 AND name = 'Фан'`, e.eventID)
	o, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, generalReq(2), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != "paid" || o.TotalTiyn != 0 || o.PaidAt == nil || len(o.Items) != 2 {
		t.Fatalf("free order = %+v, want paid with 2 items", o)
	}
	if e.events(t, events.OrderPaid, o.ID) != 1 {
		t.Error("free order must emit order.paid to issue tickets")
	}
	if a := e.availability(t); a.General[0].Available != 3 {
		t.Errorf("available = %d, want 3", a.General[0].Available)
	}
	// Платные места в том же событии по-прежнему ждут оплаты.
	p, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now())
	if err != nil || p.Status != "pending" {
		t.Errorf("paid order = %+v, %v; want pending", p, err)
	}
}

// Повторная доставка payment.succeeded после возврата не запрашивает
// второй возврат всего платежа.
func TestPaymentRedeliveryAfterRefund(t *testing.T) {
	e := newEnv(t, false, 1, 2, 0)
	ctx := t.Context()
	o, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ev := paid(o, time.Now())
	if err := e.svc.ConfirmPayment(ctx, ev); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE orders SET status = 'refunded' WHERE id = $1`, o.ID)
	if err := e.svc.ConfirmPayment(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if n := e.events(t, events.RefundRequested, o.ID); n != 0 {
		t.Errorf("refund requests after redelivery = %d, want 0", n)
	}
}

// Событие отменили, пока покупатель платил: билеты не выпускаются, деньги назад.
func TestConfirmPaymentForCancelledEvent(t *testing.T) {
	e := newEnv(t, false, 1, 2, 0)
	ctx := t.Context()
	o, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE events SET status = 'cancelled', cancelled_at = now() WHERE id = $1`, e.eventID)
	if err := e.svc.ConfirmPayment(ctx, paid(o, time.Now())); err != nil {
		t.Fatal(err)
	}
	if s := e.orderStatus(t, o.ID); s != "cancelled" || e.events(t, events.RefundRequested, o.ID) != 1 || e.events(t, events.OrderPaid, o.ID) != 0 {
		t.Errorf("order = %s; want cancelled with a refund and no tickets", s)
	}
}
