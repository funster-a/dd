package payment

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/funster-a/dd/internal/fakepsp"
	"github.com/funster-a/dd/internal/platform/events"
)

func TestPaymentSucceeds(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	order, buyer := e.order(t, "pending", 10*time.Minute)

	p, err := e.svc.StartPayment(ctx, buyer, order, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "pending" || p.AmountTiyn != total || !strings.Contains(p.PaymentURL, "/pay/") {
		t.Fatalf("payment = %+v", p)
	}
	// Повторный запрос — та же попытка и та же страница.
	again, err := e.svc.StartPayment(ctx, buyer, order, time.Now())
	if err != nil || again.ID != p.ID || again.PaymentURL != p.PaymentURL {
		t.Fatalf("second StartPayment = %+v, %v; want the same attempt", again, err)
	}

	if _, _, err := e.psp.Decide(ctx, providerID(p), fakepsp.ActionSucceed); err != nil {
		t.Fatal(err)
	}
	if s := e.paymentStatus(t, p.ID); s != "succeeded" {
		t.Fatalf("payment status = %s, want succeeded", s)
	}
	var payload []byte
	if err := e.db.Pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE topic = $1`, events.PaymentSucceeded).Scan(&payload); err != nil {
		t.Fatalf("payment.succeeded event: %v", err)
	}
	var ev events.PaymentSucceededEvent
	if err := json.Unmarshal(payload, &ev); err != nil || ev.OrderID != order || ev.PaymentID != p.ID || ev.AmountTiyn != total {
		t.Errorf("event = %+v, %v", ev, err)
	}
	if n := e.count(t, `SELECT count(*) FROM payment_webhook_events WHERE processed_at IS NOT NULL`); n != 1 {
		t.Errorf("processed webhooks = %d, want 1", n)
	}
	// Заказ ещё pending (booking подтвердит его из очереди), но второй
	// попытки быть не должно: деньги уже списаны.
	_, err = e.svc.StartPayment(ctx, buyer, order, time.Now())
	if pe, ok := errors.AsType[*PreconditionError](err); !ok || pe.Code != "order_already_paid" {
		t.Errorf("StartPayment after success = %v, want order_already_paid", err)
	}
}

// Уведомление, доставленное дважды, обрабатывается один раз.
func TestWebhookDeliveredTwice(t *testing.T) {
	e := newEnv(t)
	order, buyer := e.order(t, "pending", 10*time.Minute)
	p, err := e.svc.StartPayment(t.Context(), buyer, order, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.psp.Decide(t.Context(), providerID(p), fakepsp.ActionSucceedTwice); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM outbox WHERE topic = $1`, events.PaymentSucceeded); n != 1 {
		t.Errorf("payment.succeeded events = %d, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM payment_webhook_events`); n != 1 {
		t.Errorf("stored webhooks = %d, want 1", n)
	}
}

func TestPaymentFailedThenRetried(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	order, buyer := e.order(t, "pending", 10*time.Minute)
	first, err := e.svc.StartPayment(ctx, buyer, order, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.psp.Decide(ctx, providerID(first), fakepsp.ActionFail); err != nil {
		t.Fatal(err)
	}
	if s := e.paymentStatus(t, first.ID); s != "failed" {
		t.Fatalf("status = %s, want failed", s)
	}
	if n := e.count(t, `SELECT count(*) FROM outbox`); n != 0 {
		t.Errorf("failed payment emitted %d events", n)
	}
	second, err := e.svc.StartPayment(ctx, buyer, order, time.Now())
	if err != nil || second.ID == first.ID || second.Status != "pending" {
		t.Fatalf("retry = %+v, %v; want a new attempt", second, err)
	}
}

func TestWebhookSignature(t *testing.T) {
	e := newEnv(t)
	order, buyer := e.order(t, "pending", 10*time.Minute)
	p, err := e.svc.StartPayment(t.Context(), buyer, order, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(fakepsp.Event{ID: "evt_forged", Type: EventSucceeded, Payment: fakepsp.Payment{
		ID: providerID(p), MerchantPaymentID: p.ID, Amount: total, Status: "succeeded",
	}})
	for name, sig := range map[string]string{
		"missing":    "",
		"wrong key":  fakepsp.Sign("attacker-secret", body),
		"other body": fakepsp.Sign(secret, []byte(`{}`)),
	} {
		t.Run(name, func(t *testing.T) {
			h := http.Header{}
			h.Set("X-Signature", sig)
			if err := e.svc.HandleWebhook(t.Context(), "fakepsp", h, body, time.Now()); !errors.Is(err, ErrBadSignature) {
				t.Errorf("err = %v, want ErrBadSignature", err)
			}
		})
	}
	if s := e.paymentStatus(t, p.ID); s != "pending" {
		t.Errorf("forged webhook changed status to %s", s)
	}
	if err := e.svc.HandleWebhook(t.Context(), "stripe", http.Header{}, body, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown provider err = %v", err)
	}
	// Правильно подписанное уведомление принимается.
	h := http.Header{}
	h.Set("X-Signature", fakepsp.Sign(secret, body))
	if err := e.svc.HandleWebhook(t.Context(), "fakepsp", h, body, time.Now()); err != nil {
		t.Fatal(err)
	}
	if s := e.paymentStatus(t, p.ID); s != "succeeded" {
		t.Errorf("status = %s, want succeeded", s)
	}
}

// Провайдер недоступен: покупатель получает 502, попытка остаётся и
// подхватывается следующим запросом, когда провайдер ожил.
func TestProviderUnavailable(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	order, buyer := e.order(t, "pending", 10*time.Minute)

	e.down.Store(true)
	if _, err := e.svc.StartPayment(ctx, buyer, order, time.Now()); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}
	var created string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id FROM payments WHERE order_id = $1 AND status = 'created'`, order).Scan(&created); err != nil {
		t.Fatalf("attempt after provider failure: %v", err)
	}

	e.down.Store(false)
	p, err := e.svc.StartPayment(ctx, buyer, order, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != created || p.Status != "pending" {
		t.Errorf("payment = %+v, want the created attempt %s", p, created)
	}
}

func TestOrderNotPayable(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	code := func(err error) string {
		if pe, ok := errors.AsType[*PreconditionError](err); ok {
			return pe.Code
		}
		if errors.Is(err, ErrNotFound) {
			return "not_found"
		}
		return "unexpected: " + err.Error()
	}

	expired, b1 := e.order(t, "pending", -time.Second)
	if got := code(start(t, e, b1, expired)); got != "order_expired" {
		t.Errorf("expired order = %s", got)
	}
	cancelled, b2 := e.order(t, "cancelled", 10*time.Minute)
	if got := code(start(t, e, b2, cancelled)); got != "order_not_payable" {
		t.Errorf("cancelled order = %s", got)
	}
	mine, _ := e.order(t, "pending", 10*time.Minute)
	_, stranger := e.order(t, "pending", 10*time.Minute)
	if got := code(start(t, e, stranger, mine)); got != "not_found" {
		t.Errorf("someone else's order = %s", got)
	}
	if _, err := e.svc.StartPayment(ctx, stranger, "not-a-uuid", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad id = %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM payments`); n != 0 {
		t.Errorf("payments created for unpayable orders: %d", n)
	}
}

func start(t *testing.T, e *env, buyer, order string) error {
	_, err := e.svc.StartPayment(t.Context(), buyer, order, time.Now())
	return err
}

// Параллельные запросы оплаты одного заказа получают одну попытку.
func TestConcurrentStartPayment(t *testing.T) {
	e := newEnv(t)
	order, buyer := e.order(t, "pending", 10*time.Minute)
	const n = 20
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			p, err := e.svc.StartPayment(t.Context(), buyer, order, time.Now())
			if err != nil {
				t.Error(err)
				return
			}
			ids <- p.ID
		})
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Errorf("distinct attempts = %d, want 1", len(seen))
	}
	if c := e.count(t, `SELECT count(*) FROM payments WHERE order_id = $1`, order); c != 1 {
		t.Errorf("payments = %d, want 1", c)
	}
}

// Возврат за опоздавшую оплату: повторное событие не возвращает деньги дважды.
func TestRefundIsIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	order, buyer := e.order(t, "pending", 10*time.Minute)
	p, err := e.svc.StartPayment(ctx, buyer, order, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.psp.Decide(ctx, providerID(p), fakepsp.ActionSucceed); err != nil {
		t.Fatal(err)
	}
	ev := events.RefundRequestedEvent{PaymentID: p.ID, OrderID: order, Reason: events.RefundLatePayment}
	for range 3 {
		if err := e.svc.Refund(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.psp.Refunded(providerID(p)); got != total {
		t.Errorf("refunded at provider = %d, want %d", got, total)
	}
	if n := e.count(t, `SELECT count(*) FROM refunds WHERE payment_id = $1 AND status = 'succeeded'`, p.ID); n != 1 {
		t.Errorf("succeeded refunds = %d, want 1", n)
	}
}

// Провайдер недоступен при возврате: ошибка (worker повторит), деньги
// возвращаются при следующей попытке, и ровно один раз.
func TestRefundRetriedAfterProviderFailure(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	order, buyer := e.order(t, "pending", 10*time.Minute)
	p, err := e.svc.StartPayment(ctx, buyer, order, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.psp.Decide(ctx, providerID(p), fakepsp.ActionSucceed); err != nil {
		t.Fatal(err)
	}
	ev := events.RefundRequestedEvent{PaymentID: p.ID, OrderID: order, Reason: events.RefundLatePayment}
	e.down.Store(true)
	if err := e.svc.Refund(ctx, ev); err == nil {
		t.Fatal("refund succeeded while provider is down")
	}
	if n := e.count(t, `SELECT count(*) FROM refunds WHERE status = 'pending'`); n != 1 {
		t.Errorf("pending refunds = %d, want 1", n)
	}
	e.down.Store(false)
	if err := e.svc.Refund(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if got := e.psp.Refunded(providerID(p)); got != total {
		t.Errorf("refunded = %d, want %d", got, total)
	}
}

func TestWebhookHTTP(t *testing.T) {
	e := newEnv(t)
	order, buyer := e.order(t, "pending", 10*time.Minute)
	p, err := e.svc.StartPayment(t.Context(), buyer, order, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(fakepsp.Event{ID: "evt_1", Type: EventSucceeded, Payment: fakepsp.Payment{
		ID: providerID(p), MerchantPaymentID: p.ID, Amount: total,
	}})
	cfg := e.svc.cfg
	post := func(sig string) int {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, cfg.CallbackURL, bytes.NewReader(body))
		req.Header.Set("X-Signature", sig)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("sha256=00"); code != http.StatusUnauthorized {
		t.Errorf("bad signature = %d, want 401", code)
	}
	if code := post(fakepsp.Sign(secret, body)); code != http.StatusNoContent {
		t.Errorf("valid = %d, want 204", code)
	}
	if code := post(fakepsp.Sign(secret, body)); code != http.StatusNoContent {
		t.Errorf("repeat = %d, want 204", code)
	}
}

// ticketsOf создаёт n билетов заказа по price тиын: возврат ссылается на
// конкретные билеты (refund_items).
func (e *env) ticketsOf(t *testing.T, orderID string, n int, price int64) []string {
	t.Helper()
	ctx := t.Context()
	var org, event string
	if err := e.db.Pool.QueryRow(ctx, `SELECT organizer_id, event_id FROM orders WHERE id = $1`, orderID).Scan(&org, &event); err != nil {
		t.Fatal(err)
	}
	var cat string
	if err := e.db.Pool.QueryRow(ctx, `INSERT INTO price_categories (organizer_id, event_id, name, price_tiyn)
		VALUES ($1, $2, 'cat-' || gen_random_uuid(), $3) RETURNING id`, org, event, price).Scan(&cat); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := range n {
		var seat, item, ticket string
		if err := e.db.Pool.QueryRow(ctx, `INSERT INTO event_seats (organizer_id, event_id, price_category_id, kind, section, row_label, seat_label, status)
			VALUES ($1, $2, $3, 'seat', $4, '1', $5, 'sold') RETURNING id`, org, event, cat, cat, strconv.Itoa(i+1)).Scan(&seat); err != nil {
			t.Fatal(err)
		}
		if err := e.db.Pool.QueryRow(ctx, `INSERT INTO order_items (organizer_id, event_id, order_id, event_seat_id, price_tiyn)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`, org, event, orderID, seat, price).Scan(&item); err != nil {
			t.Fatal(err)
		}
		if err := e.db.Pool.QueryRow(ctx, `INSERT INTO tickets (organizer_id, event_id, order_id, order_item_id, event_seat_id)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`, org, event, orderID, item, seat).Scan(&ticket); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, ticket)
	}
	return ids
}

func TestRefundTickets(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	order, buyer := e.order(t, "pending", 10*time.Minute)
	p, err := e.svc.StartPayment(ctx, buyer, order, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.psp.Decide(ctx, providerID(p), fakepsp.ActionSucceed); err != nil {
		t.Fatal(err)
	}
	tickets := e.ticketsOf(t, order, 2, total/2)

	ev := events.RefundRequestedEvent{
		RequestID: uuid.NewString(), OrderID: order, Reason: events.RefundBuyerRequest,
		TicketIDs: tickets[:1], AmountTiyn: total / 2,
	}
	for range 3 { // повторная доставка
		if err := e.svc.Refund(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.psp.Refunded(providerID(p)); got != total/2 {
		t.Errorf("refunded = %d, want %d", got, total/2)
	}
	if n := e.count(t, `SELECT count(*) FROM outbox WHERE topic = $1`, events.OrderRefunded); n != 1 {
		t.Errorf("order.refunded events = %d, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM refund_items`); n != 1 {
		t.Errorf("refund items = %d, want 1", n)
	}

	// Возврат сверх суммы платежа не проходит и ничего не записывает.
	over := events.RefundRequestedEvent{
		RequestID: uuid.NewString(), OrderID: order, Reason: events.RefundBuyerRequest,
		TicketIDs: tickets[1:], AmountTiyn: total,
	}
	if err := e.svc.Refund(ctx, over); err == nil {
		t.Error("refund above the payment amount was accepted")
	}
	if got := e.psp.Refunded(providerID(p)); got != total/2 {
		t.Errorf("refunded after an excessive request = %d", got)
	}
}
