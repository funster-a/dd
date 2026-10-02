package main

import (
	"context"
	"crypto/rand"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/funster-a/dd/internal/booking"
	"github.com/funster-a/dd/internal/catalog"
	"github.com/funster-a/dd/internal/fakepsp"
	"github.com/funster-a/dd/internal/payment"
	"github.com/funster-a/dd/internal/platform/db/dbtest"
	"github.com/funster-a/dd/internal/platform/events"
	"github.com/funster-a/dd/internal/platform/mq"
	"github.com/funster-a/dd/internal/platform/outbox"
	"github.com/funster-a/dd/internal/ticket"
)

// Сквозной сценарий покупки через все модули и настоящие PostgreSQL и
// RabbitMQ (ADR 012): заказ → страница оплаты мока → вебхук → outbox →
// очередь → подтверждение заказа → выпуск билетов; и оплата после
// истечения холда → автоматический возврат.

type mailbox struct {
	mu   sync.Mutex
	sent []ticket.Mail
}

func (m *mailbox) SendTickets(_ context.Context, mail ticket.Mail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, mail)
	return nil
}

func (m *mailbox) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

type flow struct {
	db      *dbtest.DB
	book    *booking.Service
	pay     *payment.Service
	tickets *ticket.Service
	psp     *fakepsp.Server
	mail    *mailbox
	eventID string
}

func newFlow(t *testing.T) *flow {
	t.Helper()
	amqpURL := os.Getenv("RABBITMQ_TEST_URL")
	if amqpURL == "" {
		t.Skip("RABBITMQ_TEST_URL is not set")
	}
	f := &flow{db: dbtest.New(t), mail: &mailbox{}}
	log := slog.New(slog.DiscardHandler)

	var api http.Handler
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { api.ServeHTTP(w, r) }))
	t.Cleanup(apiSrv.Close)
	f.psp = fakepsp.New(fakepsp.Config{APIKey: "k", WebhookSecret: "s", RetryDelays: []time.Duration{}})
	pspSrv := httptest.NewServer(f.psp.Handler())
	t.Cleanup(pspSrv.Close)

	gw := payment.NewPSPClient(pspSrv.URL, "k", "s")
	f.book = booking.NewService(f.db.Pool, nil, log)
	f.pay = payment.NewService(f.db.Pool, gw, payment.Config{
		ReturnURL: apiSrv.URL + "/payment/return", CallbackURL: apiSrv.URL + "/v1/payments/webhooks/fakepsp",
	}, log)
	f.tickets = ticket.NewService(f.db.Pool, f.mail, ticket.Config{PublicBaseURL: apiSrv.URL, SigningKey: "flow-test-signing-key"}, log)
	r := chi.NewRouter()
	r.Route("/v1", f.pay.Register)
	api = r

	// Своя пара очередей на тест: префикс не пересекается с другими прогонами.
	prefix := "test." + strings.ToLower(rand.Text()[:8]) + "."
	conn := mq.New(amqpURL)
	pub := mq.NewPublisher(conn)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, s := range []subscription{
		subscribe(prefix, events.PaymentSucceeded, log, f.book.ConfirmPayment),
		subscribe(prefix, events.OrderPaid, log, f.tickets.Issue),
		subscribe(prefix, events.RefundRequested, log, f.pay.Refund),
		subscribe(prefix, events.OrderRefunded, log, f.book.ApplyRefund),
		subscribe(prefix, events.EventCancelled, log, f.tickets.HandleEventCancelled),
	} {
		wg.Go(func() { mq.Consume(ctx, conn, s.cfg, log, s.handler) })
	}
	relay := outbox.NewRelay(f.db.Pool, pub, prefix, log)
	relay.Interval = 20 * time.Millisecond
	wg.Go(func() { relay.Run(ctx) })
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		deleteQueues(conn, prefix)
		pub.Close()
		_ = conn.Close()
	})

	f.eventID = f.seedEvent(t)
	return f
}

func deleteQueues(conn *mq.Conn, prefix string) {
	c, err := conn.Get(context.Background())
	if err != nil {
		return
	}
	ch, err := c.Channel()
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	for _, topic := range []string{events.PaymentSucceeded, events.OrderPaid, events.RefundRequested,
		events.OrderRefunded, events.EventCancelled} {
		_, _ = ch.QueueDelete(prefix+topic, false, false, false)
		_, _ = ch.QueueDelete(prefix+topic+".dead", false, false, false)
	}
}

func (f *flow) seedEvent(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	p := f.db.Pool
	var org, venue, seatMap, event, cat string
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(p.QueryRow(ctx, `INSERT INTO organizers (name, slug) VALUES ('Org', $1) RETURNING id`,
		"org-"+strings.ToLower(rand.Text()[:8])).Scan(&org))
	must(p.QueryRow(ctx, `INSERT INTO venues (organizer_id, name) VALUES ($1, 'Hall') RETURNING id`, org).Scan(&venue))
	must(p.QueryRow(ctx, `INSERT INTO seat_maps (organizer_id, venue_id, name, layout) VALUES ($1, $2, 'Main', '{}') RETURNING id`,
		org, venue).Scan(&seatMap))
	start := time.Now().Add(30 * 24 * time.Hour)
	must(p.QueryRow(ctx, `INSERT INTO events (organizer_id, venue_id, seat_map_id, slug, title, status, starts_at, ends_at, published_at)
		VALUES ($1, $2, $3, 'show', 'Show', 'published', $4, $5, now()) RETURNING id`, org, venue, seatMap, start, start.Add(2*time.Hour)).Scan(&event))
	must(p.QueryRow(ctx, `INSERT INTO price_categories (organizer_id, event_id, name, price_tiyn) VALUES ($1, $2, 'Партер', 500000) RETURNING id`,
		org, event).Scan(&cat))
	_, err := p.Exec(ctx, `INSERT INTO event_seats (organizer_id, event_id, price_category_id, kind, section, row_label, seat_label)
		SELECT $1, $2, $3, 'seat', 'Партер', '1', n::text FROM generate_series(1, 4) n`, org, event, cat)
	must(err)
	return event
}

func (f *flow) buyer(t *testing.T) string {
	t.Helper()
	n, _ := rand.Int(rand.Reader, big.NewInt(9_000_000_000))
	var id string
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO buyers (phone) VALUES ($1) RETURNING id`,
		"+7"+strconv.FormatInt(1_000_000_000+n.Int64(), 10)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *flow) order(t *testing.T, buyer string, seats ...string) booking.Order {
	t.Helper()
	req := booking.OrderRequest{Email: "buyer@example.com"}
	for _, s := range seats {
		req.Seats = append(req.Seats, booking.SeatRef{Section: "Партер", Row: "1", Seat: s})
	}
	o, err := f.book.CreateOrder(t.Context(), buyer, f.eventID, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func providerID(p payment.Payment) string {
	return p.PaymentURL[strings.LastIndex(p.PaymentURL, "/")+1:]
}

func TestPurchaseFlow(t *testing.T) {
	f := newFlow(t)
	ctx := t.Context()
	buyer := f.buyer(t)
	o := f.order(t, buyer, "1", "2")

	p, err := f.pay.StartPayment(ctx, buyer, o.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Покупатель нажимает «Оплатить» на странице провайдера; провайдер
	// присылает уведомление дважды.
	if _, _, err := f.psp.Decide(ctx, providerID(p), fakepsp.ActionSucceedTwice); err != nil {
		t.Fatal(err)
	}

	var ts []ticket.Ticket
	eventually(t, "tickets", func() bool {
		ts, err = f.tickets.ForOrder(ctx, buyer, o.ID)
		return err == nil && len(ts) == 2
	})
	got, err := f.book.GetOrder(ctx, buyer, o.ID, time.Now())
	if err != nil || got.Status != "paid" || got.PaidAt == nil {
		t.Fatalf("order = %+v, %v; want paid", got, err)
	}
	// Уведомление пришло дважды, а письмо и билеты — по одному разу.
	time.Sleep(200 * time.Millisecond)
	if f.mail.count() != 1 {
		t.Errorf("emails = %d, want 1", f.mail.count())
	}
	var n int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE order_id = $1`, o.ID).Scan(&n); err != nil || n != 2 {
		t.Errorf("tickets = %d, %v; want 2", n, err)
	}
	if v, err := f.tickets.ByToken(ctx, ts[0].URL[strings.LastIndex(ts[0].URL, "/")+1:]); err != nil || v.Status != "issued" {
		t.Errorf("ticket by link = %+v, %v", v, err)
	}
}

func TestLatePaymentIsRefunded(t *testing.T) {
	f := newFlow(t)
	ctx := t.Context()
	buyer := f.buyer(t)
	o := f.order(t, buyer, "3")
	p, err := f.pay.StartPayment(ctx, buyer, o.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Покупатель ушёл со страницы оплаты и вернулся после конца холда.
	if _, err := f.db.Pool.Exec(ctx, `UPDATE orders SET expires_at = now() - interval '1 second' WHERE id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE event_seats SET hold_expires_at = now() - interval '1 second' WHERE hold_order_id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.psp.Decide(ctx, providerID(p), fakepsp.ActionSucceed); err != nil {
		t.Fatal(err)
	}

	eventually(t, "refund at provider", func() bool { return f.psp.Refunded(providerID(p)) == o.TotalTiyn })
	// Провайдер фиксирует возврат раньше, чем платёжный модуль получает ответ
	// и записывает succeeded, поэтому статус в базе тоже ждём.
	var refund string
	eventually(t, "refund succeeded in the database", func() bool {
		return f.db.Pool.QueryRow(ctx, `SELECT status FROM refunds WHERE payment_id = $1`, p.ID).Scan(&refund) == nil && refund == "succeeded"
	})
	got, err := f.book.GetOrder(ctx, buyer, o.ID, time.Now())
	if err != nil || got.Status != "expired" {
		t.Errorf("order = %+v, %v; want expired", got, err)
	}
	if ts, _ := f.tickets.ForOrder(ctx, buyer, o.ID); len(ts) != 0 {
		t.Errorf("tickets issued for a refunded order: %d", len(ts))
	}
}

// Бесплатные билеты: оплаты нет, заказ оформлен сразу, билеты выпускает
// тот же конвейер событий.
func TestFreeTicketsFlow(t *testing.T) {
	f := newFlow(t)
	ctx := t.Context()
	if _, err := f.db.Pool.Exec(ctx, `UPDATE price_categories SET price_tiyn = 0 WHERE event_id = $1`, f.eventID); err != nil {
		t.Fatal(err)
	}
	buyer := f.buyer(t)
	o := f.order(t, buyer, "4")
	if o.Status != "paid" || o.TotalTiyn != 0 {
		t.Fatalf("free order = %+v, want paid", o)
	}
	eventually(t, "free tickets", func() bool {
		ts, err := f.tickets.ForOrder(ctx, buyer, o.ID)
		return err == nil && len(ts) == 1
	})
	if _, err := f.pay.StartPayment(ctx, buyer, o.ID, time.Now()); err == nil {
		t.Error("payment started for a free paid order")
	}
}

// buyPaid покупает места и ждёт выпуска билетов.
func (f *flow) buyPaid(t *testing.T, buyer string, seats ...string) (booking.Order, payment.Payment, []ticket.Ticket) {
	t.Helper()
	ctx := t.Context()
	o := f.order(t, buyer, seats...)
	p, err := f.pay.StartPayment(ctx, buyer, o.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.psp.Decide(ctx, providerID(p), fakepsp.ActionSucceed); err != nil {
		t.Fatal(err)
	}
	var ts []ticket.Ticket
	eventually(t, "tickets", func() bool {
		ts, err = f.tickets.ForOrder(ctx, buyer, o.ID)
		return err == nil && len(ts) == len(seats)
	})
	return o, p, ts
}

func (f *flow) orderStatus(t *testing.T, id string) string {
	t.Helper()
	var s string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT status FROM orders WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Покупатель возвращает билеты по одному: деньги за каждый возвращаются,
// места снова продаются, заказ становится частично, затем полностью
// возвращённым.
func TestBuyerRefundFlow(t *testing.T) {
	f := newFlow(t)
	ctx := t.Context()
	buyer := f.buyer(t)
	o, p, ts := f.buyPaid(t, buyer, "1", "2")

	req, err := f.tickets.RequestRefund(ctx, buyer, o.ID, []string{ts[0].ID}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if req.AmountTiyn != 500000 {
		t.Errorf("refund amount = %d, want 500000", req.AmountTiyn)
	}
	eventually(t, "partial refund", func() bool {
		return f.psp.Refunded(providerID(p)) == 500000 && f.orderStatus(t, o.ID) == "partially_refunded"
	})
	// Место возвращённого билета снова продаётся.
	if _, err := f.book.CreateOrder(ctx, f.buyer(t), f.eventID, booking.OrderRequest{
		Seats: []booking.SeatRef{{Section: "Партер", Row: "1", Seat: ts[0].Seat}}, Email: "next@example.com",
	}, time.Now()); err != nil {
		t.Errorf("refunded seat is not for sale again: %v", err)
	}
	if _, err := f.tickets.RequestRefund(ctx, buyer, o.ID, []string{ts[0].ID}, time.Now()); err == nil {
		t.Error("the same ticket was refunded twice")
	}

	if _, err := f.tickets.RequestRefund(ctx, buyer, o.ID, []string{ts[1].ID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "full refund", func() bool {
		return f.psp.Refunded(providerID(p)) == o.TotalTiyn && f.orderStatus(t, o.ID) == "refunded"
	})
	var n int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE order_id = $1 AND status = 'succeeded'`, o.ID).Scan(&n); err != nil || n != 2 {
		t.Errorf("succeeded refunds = %d, %v; want 2", n, err)
	}
}

// Организатор отменяет событие: билеты аннулируются, всем покупателям деньги
// возвращаются полностью, в том числе за уже частично возвращённый заказ.
func TestEventCancelledFlow(t *testing.T) {
	f := newFlow(t)
	ctx := t.Context()
	a, b := f.buyer(t), f.buyer(t)
	oa, pa, tsa := f.buyPaid(t, a, "1", "2")
	ob, pb, _ := f.buyPaid(t, b, "3")
	if _, err := f.tickets.RequestRefund(ctx, a, oa.ID, []string{tsa[0].ID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "partial refund", func() bool { return f.orderStatus(t, oa.ID) == "partially_refunded" })

	var org string
	if err := f.db.Pool.QueryRow(ctx, `SELECT organizer_id FROM events WHERE id = $1`, f.eventID).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.NewService(f.db.Pool).CancelEvent(ctx, org, f.eventID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "refunds for the cancelled event", func() bool {
		return f.psp.Refunded(providerID(pa)) == oa.TotalTiyn && f.psp.Refunded(providerID(pb)) == ob.TotalTiyn &&
			f.orderStatus(t, oa.ID) == "refunded" && f.orderStatus(t, ob.ID) == "refunded"
	})
	var active int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE event_id = $1 AND status <> 'revoked'`, f.eventID).Scan(&active); err != nil || active != 0 {
		t.Errorf("active tickets after cancellation = %d, %v", active, err)
	}
	// Покупать и платить за отменённое событие нельзя.
	if _, err := f.book.CreateOrder(ctx, f.buyer(t), f.eventID, booking.OrderRequest{
		Seats: []booking.SeatRef{{Section: "Партер", Row: "1", Seat: "4"}}, Email: "x@example.com",
	}, time.Now()); err == nil {
		t.Error("order for a cancelled event")
	}
}
