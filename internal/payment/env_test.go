package payment

import (
	"context"
	"crypto/rand"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/funster-a/dd/internal/fakepsp"
	"github.com/funster-a/dd/internal/platform/db/dbtest"
)

// Интеграционные тесты: нужен DATABASE_TEST_URL. Мок провайдера и вебхук
// платформы работают настоящими HTTP-серверами. Заказ создаётся прямо SQL:
// payment не импортирует booking.

const (
	apiKey = "test-api-key"
	secret = "test-webhook-secret" //nolint:gosec // тестовый секрет мока
	total  = int64(700_000)
)

type env struct {
	svc  *Service
	db   *dbtest.DB
	psp  *fakepsp.Server
	down atomic.Bool // провайдер отвечает 503
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{db: dbtest.New(t)}
	log := slog.New(slog.DiscardHandler)

	var api http.Handler
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { api.ServeHTTP(w, r) }))
	t.Cleanup(apiSrv.Close)

	e.psp = fakepsp.New(fakepsp.Config{APIKey: apiKey, WebhookSecret: secret, RetryDelays: []time.Duration{}})
	pspHandler := e.psp.Handler()
	pspSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		pspHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(pspSrv.Close)
	t.Cleanup(e.psp.Wait)

	e.svc = NewService(e.db.Pool, NewPSPClient(pspSrv.URL, apiKey, secret), Config{
		ReturnURL:   apiSrv.URL + "/payment/return",
		CallbackURL: apiSrv.URL + "/v1/payments/webhooks/fakepsp",
	}, log)
	r := chi.NewRouter()
	r.Route("/v1", e.svc.Register)
	api = r
	return e
}

// order создаёт заказ status с суммой total, истекающий через ttl.
func (e *env) order(t *testing.T, status string, ttl time.Duration) (orderID, buyerID string) {
	t.Helper()
	ctx := t.Context()
	p := e.db.Pool
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var org, venue, seatMap, event string
	must(p.QueryRow(ctx, `INSERT INTO organizers (name, slug) VALUES ('Org', $1) RETURNING id`,
		"org-"+strings.ToLower(rand.Text()[:8])).Scan(&org))
	must(p.QueryRow(ctx, `INSERT INTO venues (organizer_id, name) VALUES ($1, 'Hall') RETURNING id`, org).Scan(&venue))
	must(p.QueryRow(ctx, `INSERT INTO seat_maps (organizer_id, venue_id, name, layout) VALUES ($1, $2, 'Main', '{}') RETURNING id`,
		org, venue).Scan(&seatMap))
	start := time.Now().Add(30 * 24 * time.Hour)
	must(p.QueryRow(ctx, `INSERT INTO events (organizer_id, venue_id, seat_map_id, slug, title, status, starts_at, ends_at, published_at)
		VALUES ($1, $2, $3, 'show', 'Стендап', 'published', $4, $5, now()) RETURNING id`,
		org, venue, seatMap, start, start.Add(2*time.Hour)).Scan(&event))
	n, _ := rand.Int(rand.Reader, big.NewInt(9_000_000_000))
	must(p.QueryRow(ctx, `INSERT INTO buyers (phone) VALUES ($1) RETURNING id`,
		"+7"+strconv.FormatInt(1_000_000_000+n.Int64(), 10)).Scan(&buyerID))
	must(p.QueryRow(ctx, `INSERT INTO orders (organizer_id, event_id, buyer_id, status, email, total_tiyn, expires_at)
		VALUES ($1, $2, $3, $4, 'b@example.com', $5, $6) RETURNING id`,
		org, event, buyerID, status, total, time.Now().Add(ttl)).Scan(&orderID))
	return orderID, buyerID
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) paymentStatus(t *testing.T, id string) string {
	t.Helper()
	var s string
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT status FROM payments WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// providerID — id платежа у провайдера из ссылки на страницу оплаты.
func providerID(p Payment) string { return p.PaymentURL[strings.LastIndex(p.PaymentURL, "/")+1:] }
