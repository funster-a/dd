package ticket

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/funster-a/dd/internal/platform/auth"
	"github.com/funster-a/dd/internal/platform/db/dbtest"
	"github.com/funster-a/dd/internal/platform/events"
)

// Интеграционные тесты: нужен DATABASE_TEST_URL. Оплаченный заказ
// создаётся прямо SQL: ticket не импортирует booking.

type fakeMailer struct {
	mu   sync.Mutex
	sent []Mail
}

func (m *fakeMailer) SendTickets(_ context.Context, mail Mail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, mail)
	return nil
}

type env struct {
	svc    *Service
	db     *dbtest.DB
	mailer *fakeMailer
	event  string
	org    string
	seats  []string
	cat    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{db: dbtest.New(t), mailer: &fakeMailer{}}
	e.svc = NewService(e.db.Pool, e.mailer, Config{PublicBaseURL: "https://tickets.test/", SigningKey: "test-signing-key-1234"},
		slog.New(slog.DiscardHandler))
	ctx := t.Context()
	p := e.db.Pool
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var venue, seatMap string
	must(p.QueryRow(ctx, `INSERT INTO organizers (name, slug) VALUES ('Org', $1) RETURNING id`,
		"org-"+strings.ToLower(rand.Text()[:8])).Scan(&e.org))
	must(p.QueryRow(ctx, `INSERT INTO venues (organizer_id, name, address) VALUES ($1, 'Клуб', 'Абая, 1') RETURNING id`, e.org).Scan(&venue))
	must(p.QueryRow(ctx, `INSERT INTO seat_maps (organizer_id, venue_id, name, layout) VALUES ($1, $2, 'Main', '{}') RETURNING id`,
		e.org, venue).Scan(&seatMap))
	start := time.Now().Add(30 * 24 * time.Hour)
	must(p.QueryRow(ctx, `INSERT INTO events (organizer_id, venue_id, seat_map_id, slug, title, status, starts_at, ends_at, published_at, age_rating)
		VALUES ($1, $2, $3, 'show', 'Стендап <вечер>', 'published', $4, $5, now(), '18+') RETURNING id`,
		e.org, venue, seatMap, start, start.Add(2*time.Hour)).Scan(&e.event))
	must(p.QueryRow(ctx, `INSERT INTO price_categories (organizer_id, event_id, name, price_tiyn) VALUES ($1, $2, 'Партер', 500000) RETURNING id`,
		e.org, e.event).Scan(&e.cat))
	rows, err := p.Query(ctx, `INSERT INTO event_seats (organizer_id, event_id, price_category_id, kind, section, row_label, seat_label, status)
		SELECT $1, $2, $3, 'seat', 'Партер', '1', n::text, 'sold' FROM generate_series(1, 3) n RETURNING id`, e.org, e.event, e.cat)
	must(err)
	for rows.Next() {
		var id string
		must(rows.Scan(&id))
		e.seats = append(e.seats, id)
	}
	must(rows.Err())
	return e
}

// order создаёт заказ со статусом status на места seats.
// testFee — сервисный сбор 5 % с билета за 5 000 ₸ (ADR 019).
const testFee int64 = 25000

// order создаёт заказ с билетами по 5 000 ₸ и сервисным сбором testFee.
func (e *env) order(t *testing.T, status string, seats ...string) (orderID, buyerID string) {
	t.Helper()
	ctx := t.Context()
	p := e.db.Pool
	n, _ := rand.Int(rand.Reader, big.NewInt(9_000_000_000))
	if err := p.QueryRow(ctx, `INSERT INTO buyers (phone) VALUES ($1) RETURNING id`,
		"+7"+strconv.FormatInt(1_000_000_000+n.Int64(), 10)).Scan(&buyerID); err != nil {
		t.Fatal(err)
	}
	var paidAt any
	if status == "paid" {
		paidAt = time.Now()
	}
	if err := p.QueryRow(ctx, `INSERT INTO orders (organizer_id, event_id, buyer_id, status, email, total_tiyn, expires_at, paid_at)
		VALUES ($1, $2, $3, $4, 'buyer@example.com', $5, now() + interval '10 minutes', $6) RETURNING id`,
		e.org, e.event, buyerID, status, int64(len(seats))*(500000+testFee), paidAt).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	for _, s := range seats {
		if _, err := p.Exec(ctx, `INSERT INTO order_items (organizer_id, event_id, order_id, event_seat_id, price_tiyn, fee_tiyn)
			VALUES ($1, $2, $3, $4, 500000, $5)`, e.org, e.event, orderID, s, testFee); err != nil {
			t.Fatal(err)
		}
	}
	return orderID, buyerID
}

func tokenOf(url string) string { return url[strings.LastIndex(url, "/")+1:] }

func TestIssueTickets(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	order, buyer := e.order(t, "paid", e.seats[0], e.seats[1])

	for range 3 { // повторная доставка order.paid
		if err := e.svc.Issue(ctx, events.OrderPaidEvent{OrderID: order}); err != nil {
			t.Fatal(err)
		}
	}
	ts, err := e.svc.ForOrder(ctx, buyer, order)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 || ts[0].Status != "issued" || *ts[0].Row != "1" || ts[0].Seat != "1" {
		t.Fatalf("tickets = %+v", ts)
	}
	if !strings.HasPrefix(ts[0].URL, "https://tickets.test/t/") {
		t.Errorf("url = %s", ts[0].URL)
	}
	if len(e.mailer.sent) != 1 || e.mailer.sent[0].To != "buyer@example.com" || len(e.mailer.sent[0].Links) != 2 {
		t.Errorf("mails = %+v, want one mail with two links", e.mailer.sent)
	}

	if _, err := e.svc.ForOrder(ctx, uuid.NewString(), order); !errors.Is(err, ErrNotFound) {
		t.Errorf("stranger ForOrder = %v, want ErrNotFound", err)
	}

	pending, _ := e.order(t, "pending", e.seats[2])
	if err := e.svc.Issue(ctx, events.OrderPaidEvent{OrderID: pending}); err == nil {
		t.Error("issued tickets for an unpaid order")
	}
}

// Главный инвариант на последнем рубеже: даже если бы место оказалось в
// двух оплаченных заказах, второй действующий билет не выпускается.
func TestSecondTicketForSeatRejected(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	first, _ := e.order(t, "paid", e.seats[0])
	second, _ := e.order(t, "paid", e.seats[0])
	if err := e.svc.Issue(ctx, events.OrderPaidEvent{OrderID: first}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Issue(ctx, events.OrderPaidEvent{OrderID: second}); err == nil {
		t.Fatal("second ticket for the same seat was issued")
	}
	var n int
	if err := e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE event_seat_id = $1`, e.seats[0]).Scan(&n); err != nil || n != 1 {
		t.Errorf("tickets for seat = %d, %v; want 1", n, err)
	}
}

func TestTicketToken(t *testing.T) {
	s := NewSigner("key-one-1234567890")
	id := uuid.NewString()
	tok := s.Token(id)
	if got, ok := s.Parse(tok); !ok || got != id {
		t.Fatalf("Parse(Token) = %s, %v", got, ok)
	}
	b := []byte(tok)
	b[len(b)-1] ^= 1 // меняет только «лишние» биты последнего символа
	flipped := []byte(tok)
	flipped[5] ^= 1
	for name, bad := range map[string]string{
		"non-canonical": string(b),
		"tampered":      string(flipped),
		"other key":     NewSigner("key-two-1234567890").Token(id),
		"garbage":       "not-a-token",
		"empty":         "",
	} {
		if _, ok := s.Parse(bad); ok {
			t.Errorf("%s token accepted", name)
		}
	}
}

func TestTicketHTTP(t *testing.T) {
	e := newEnv(t)
	order, buyer := e.order(t, "paid", e.seats[0])
	if err := e.svc.Issue(t.Context(), events.OrderPaidEvent{OrderID: order}); err != nil {
		t.Fatal(err)
	}
	ts, err := e.svc.ForOrder(t.Context(), buyer, order)
	if err != nil {
		t.Fatal(err)
	}
	tok := tokenOf(ts[0].URL)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id := r.Header.Get("X-Test-Buyer"); id != "" {
				r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindBuyer, SubjectID: id}))
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/t/{token}", e.svc.HandlePage)
	r.Route("/v1", e.svc.Register)
	srv := httptest.NewServer(r)
	defer srv.Close()
	get := func(path, buyer string) (int, http.Header, []byte) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if buyer != "" {
			req.Header.Set("X-Test-Buyer", buyer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, resp.Header, buf.Bytes()
	}

	if code, _, body := get("/v1/orders/"+order+"/tickets", buyer); code != http.StatusOK || !bytes.Contains(body, []byte(tok)) {
		t.Errorf("order tickets = %d %s", code, body)
	}
	if code, _, _ := get("/v1/orders/"+order+"/tickets", ""); code != http.StatusUnauthorized {
		t.Errorf("anonymous order tickets = %d, want 401", code)
	}
	if code, _, body := get("/v1/tickets/"+tok, ""); code != http.StatusOK || !bytes.Contains(body, []byte(`"status":"issued"`)) {
		t.Errorf("ticket = %d %s", code, body)
	}
	code, hdr, body := get("/v1/tickets/"+tok+"/qr.png", "")
	if code != http.StatusOK || hdr.Get("Content-Type") != "image/png" || !bytes.HasPrefix(body, []byte("\x89PNG")) {
		t.Errorf("qr = %d %s, %d bytes", code, hdr.Get("Content-Type"), len(body))
	}
	code, _, body = get("/t/"+tok, "")
	if code != http.StatusOK || !bytes.Contains(body, []byte("Стендап &lt;вечер&gt;")) || !bytes.Contains(body, []byte("ряд 1, место 1")) {
		t.Errorf("page = %d %s", code, body)
	}
	// Возврат билета покупателем: 202, билет сразу аннулирован.
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/v1/orders/"+order+"/refunds",
		strings.NewReader(`{"ticket_ids":["`+ts[0].ID+`"]}`))
	req.Header.Set("X-Test-Buyer", buyer)
	req.Header.Set("Idempotency-Key", uuid.NewString())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("refund = %d, want 202", resp.StatusCode)
	}
	if code, _, body := get("/v1/tickets/"+tok, ""); code != http.StatusOK || !bytes.Contains(body, []byte(`"status":"revoked"`)) {
		t.Errorf("ticket after refund = %d %s", code, body)
	}
	if code, _, _ := get("/v1/tickets/forged", ""); code != http.StatusNotFound {
		t.Errorf("forged token = %d, want 404", code)
	}
	if code, _, _ := get("/t/forged", ""); code != http.StatusNotFound {
		t.Errorf("forged page = %d, want 404", code)
	}
}
