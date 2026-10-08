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

// Возвращённое место сразу снова продаётся: холд оплаченного заказа в
// Redis снимается вместе с освобождением места в базе.
func TestRefundedSeatIsBookableAgain(t *testing.T) {
	e := newEnv(t, true, 1, 2, 0)
	ctx := t.Context()
	o, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConfirmPayment(ctx, paid(o, time.Now())); err != nil {
		t.Fatal(err)
	}
	var ticketID string
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO tickets (organizer_id, event_id, order_id, order_item_id, event_seat_id, status, revoked_at)
		SELECT organizer_id, $1, order_id, id, event_seat_id, 'revoked', now() FROM order_items WHERE order_id = $2
		RETURNING id`, e.eventID, o.ID).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ApplyRefund(ctx, events.OrderRefundedEvent{OrderID: o.ID, TicketIDs: []string{ticketID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now()); err != nil {
		t.Errorf("refunded seat must be bookable at once: %v", err)
	}
}

// refundTicket оставляет билет заказа так, как его оставляют модули ticket и
// payment перед order.refunded: билет аннулирован, возврат за него прошёл.
// Возвращает id билета.
func (e *env) refundTicket(t *testing.T, o Order) string {
	t.Helper()
	ctx := t.Context()
	var ticketID string
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO tickets (organizer_id, event_id, order_id, order_item_id, event_seat_id, status, revoked_at)
		SELECT organizer_id, $1, order_id, id, event_seat_id, 'revoked', now() FROM order_items WHERE order_id = $2
		RETURNING id`, e.eventID, o.ID).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `WITH p AS (
		  INSERT INTO payments (organizer_id, order_id, status, amount_tiyn, provider)
		  SELECT organizer_id, id, 'succeeded', total_tiyn, 'fakepsp' FROM orders WHERE id = $1 RETURNING id, organizer_id, order_id, amount_tiyn),
		r AS (
		  INSERT INTO refunds (organizer_id, payment_id, order_id, status, amount_tiyn, reason)
		  SELECT organizer_id, id, order_id, 'succeeded', amount_tiyn, 'buyer_request' FROM p RETURNING id, organizer_id, order_id)
		INSERT INTO refund_items (refund_id, ticket_id, organizer_id, order_id) SELECT id, $2, organizer_id, order_id FROM r`,
		o.ID, ticketID)
	return ticketID
}

// Повторная доставка order.refunded (доставка «хотя бы один раз», ADR 012)
// не освобождает место, которое после возврата уже купил и оплатил другой
// покупатель: иначе место ушло бы в продажу третий раз при действующем
// билете второго (CLAUDE.md, правила 1 и 3).
func TestRefundRedeliveryKeepsResoldSeat(t *testing.T) {
	for name, withRedis := range modes() {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withRedis, 1, 2, 0)
			ctx := t.Context()
			a, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := e.svc.ConfirmPayment(ctx, paid(a, time.Now())); err != nil {
				t.Fatal(err)
			}
			refunded := events.OrderRefundedEvent{OrderID: a.ID, TicketIDs: []string{e.refundTicket(t, a)}}
			if err := e.svc.ApplyRefund(ctx, refunded); err != nil {
				t.Fatal(err)
			}

			g, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now())
			if err != nil {
				t.Fatalf("refunded seat must be bookable: %v", err)
			}
			if err := e.svc.ConfirmPayment(ctx, paid(g, time.Now())); err != nil {
				t.Fatal(err)
			}

			if err := e.svc.ApplyRefund(ctx, refunded); err != nil {
				t.Fatal(err)
			}
			if s, _ := e.seatStatus(t, seat(1, 1)); s != "sold" {
				t.Errorf("seat after redelivered refund = %s, want sold to the second buyer", s)
			}
			// Второй покупатель вернул билет, но деньги ещё не вернулись: место
			// по-прежнему за ним (ADR 014), и повтор первого возврата его не трогает.
			e.exec(t, `INSERT INTO tickets (organizer_id, event_id, order_id, order_item_id, event_seat_id, status, revoked_at)
				SELECT organizer_id, $1, order_id, id, event_seat_id, 'revoked', now() FROM order_items WHERE order_id = $2`,
				e.eventID, g.ID)
			if err := e.svc.ApplyRefund(ctx, refunded); err != nil {
				t.Fatal(err)
			}
			if s, _ := e.seatStatus(t, seat(1, 1)); s != "sold" {
				t.Errorf("seat while the second refund is in flight = %s, want sold", s)
			}
			if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now()); err == nil {
				t.Error("a third buyer got a seat that is sold to the second one")
			}
		})
	}
}

// То же под гонкой: повторы order.refunded идут параллельно с покупкой и
// оплатой места вторым покупателем. Если второй заказ оплачен, место
// остаётся проданным, сколько бы повторов ни пришло и в каком порядке.
func TestRefundRedeliveryRacesResale(t *testing.T) {
	for name, withRedis := range modes() {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withRedis, 1, 2, 0)
			ctx := t.Context()
			for round := range 5 {
				a, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now())
				if err != nil {
					t.Fatalf("round %d: %v", round, err)
				}
				if err := e.svc.ConfirmPayment(ctx, paid(a, time.Now())); err != nil {
					t.Fatal(err)
				}
				refunded := events.OrderRefundedEvent{OrderID: a.ID, TicketIDs: []string{e.refundTicket(t, a)}}
				if err := e.svc.ApplyRefund(ctx, refunded); err != nil {
					t.Fatal(err)
				}

				var wg sync.WaitGroup
				for range 8 {
					wg.Go(func() {
						if err := e.svc.ApplyRefund(ctx, refunded); err != nil {
							t.Error(err)
						}
					})
				}
				var g Order
				second := e.buyer(t)
				wg.Go(func() {
					var err error
					if g, err = e.svc.CreateOrder(ctx, second, e.eventID, seatsReq(seat(1, 1)), time.Now()); err != nil {
						t.Error(err)
						return
					}
					if err := e.svc.ConfirmPayment(ctx, paid(g, time.Now())); err != nil {
						t.Error(err)
					}
				})
				wg.Wait()
				if t.Failed() {
					return
				}
				if s := e.orderStatus(t, g.ID); s != "paid" {
					t.Fatalf("round %d: second order = %s, want paid", round, s)
				}
				if s, _ := e.seatStatus(t, seat(1, 1)); s != "sold" {
					t.Fatalf("round %d: seat = %s, want sold to the second buyer", round, s)
				}
				// Следующий раунд: второй покупатель тоже возвращает билет, и
				// место снова свободно, хотя у первого заказа на нём позиция.
				if err := e.svc.ApplyRefund(ctx, events.OrderRefundedEvent{OrderID: g.ID, TicketIDs: []string{e.refundTicket(t, g)}}); err != nil {
					t.Fatal(err)
				}
				if s, _ := e.seatStatus(t, seat(1, 1)); s != "available" {
					t.Fatalf("round %d: seat after the second refund = %s, want available", round, s)
				}
			}
		})
	}
}

// Прежний владелец вернул только часть заказа: его заказ остаётся
// partially_refunded с позицией на месте, за которое деньги уже вернулись.
// Такая позиция места не держит: следующий владелец вернул билет — место
// снова продаётся.
func TestRefundAfterPartialRefundOfPreviousOwner(t *testing.T) {
	e := newEnv(t, false, 1, 3, 0)
	ctx := t.Context()
	a, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1), seat(1, 2)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConfirmPayment(ctx, paid(a, time.Now())); err != nil {
		t.Fatal(err)
	}
	var ticketID string
	if err := e.db.Pool.QueryRow(ctx, `
		WITH issued AS (
		  INSERT INTO tickets (organizer_id, event_id, order_id, order_item_id, event_seat_id)
		  SELECT i.organizer_id, $1, i.order_id, i.id, i.event_seat_id FROM order_items i WHERE i.order_id = $2
		  RETURNING id, event_seat_id)
		SELECT issued.id FROM issued JOIN event_seats s ON s.id = issued.event_seat_id WHERE s.seat_label = '1'`,
		e.eventID, a.ID).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE tickets SET status = 'revoked', revoked_at = now() WHERE id = $1`, ticketID)
	e.exec(t, `WITH p AS (
		  INSERT INTO payments (organizer_id, order_id, status, amount_tiyn, provider)
		  SELECT organizer_id, id, 'succeeded', total_tiyn, 'fakepsp' FROM orders WHERE id = $1 RETURNING id, organizer_id, order_id),
		r AS (
		  INSERT INTO refunds (organizer_id, payment_id, order_id, status, amount_tiyn, reason)
		  SELECT organizer_id, id, order_id, 'succeeded', 1, 'buyer_request' FROM p RETURNING id, organizer_id, order_id)
		INSERT INTO refund_items (refund_id, ticket_id, organizer_id, order_id) SELECT id, $2, organizer_id, order_id FROM r`,
		a.ID, ticketID)
	if err := e.svc.ApplyRefund(ctx, events.OrderRefundedEvent{OrderID: a.ID, TicketIDs: []string{ticketID}}); err != nil {
		t.Fatal(err)
	}
	if s := e.orderStatus(t, a.ID); s != "partially_refunded" {
		t.Fatalf("first order = %s, want partially_refunded", s)
	}

	g, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConfirmPayment(ctx, paid(g, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ApplyRefund(ctx, events.OrderRefundedEvent{OrderID: g.ID, TicketIDs: []string{e.refundTicket(t, g)}}); err != nil {
		t.Fatal(err)
	}
	if s, _ := e.seatStatus(t, seat(1, 1)); s != "available" {
		t.Errorf("seat after the second owner's refund = %s, want available", s)
	}
}
