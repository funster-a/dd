package migrations_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/funster-a/dd/internal/platform/db/dbtest"
)

// Коды ошибок PostgreSQL, на которые опираются инварианты схемы.
const (
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
	checkViolation      = "23514"
)

func TestMigrationsRoundTrip(t *testing.T) {
	db := dbtest.NewEmpty(t)
	provider := dbtest.Provider(t, db.URL)
	ctx := t.Context()

	for _, step := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"up", func(ctx context.Context) error { _, err := provider.Up(ctx); return err }},
		{"down to zero", func(ctx context.Context) error { _, err := provider.DownTo(ctx, 0); return err }},
		{"up again", func(ctx context.Context) error { _, err := provider.Up(ctx); return err }},
	} {
		if err := step.run(ctx); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
	}
}

// TestOneActiveTicketPerSeatUnderConcurrency — главный инвариант на уровне
// базы: сколько бы транзакций ни пытались выпустить билет на одно место,
// успешна ровно одна.
func TestOneActiveTicketPerSeatUnderConcurrency(t *testing.T) {
	db := dbtest.New(t)
	f := newFixture(t, db.Pool)
	seat := f.seat(t)

	const buyers = 50
	items := make([]string, buyers)
	for i := range items {
		order := f.order(t, f.buyer(t))
		items[i] = f.orderItem(t, order, seat)
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		issued  int
		unknown []error
	)
	start := make(chan struct{})
	for _, item := range items {
		wg.Go(func() {
			<-start
			err := f.issueTicket(context.Background(), item)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				issued++
			case pgCode(err) == uniqueViolation:
			default:
				unknown = append(unknown, err)
			}
		})
	}
	close(start)
	wg.Wait()

	if len(unknown) > 0 {
		t.Fatalf("unexpected errors: %v", unknown)
	}
	if issued != 1 {
		t.Fatalf("issued %d tickets for one seat, want exactly 1", issued)
	}
}

func TestRevokedTicketFreesSeat(t *testing.T) {
	db := dbtest.New(t)
	f := newFixture(t, db.Pool)
	seat := f.seat(t)

	first := f.orderItem(t, f.order(t, f.buyer(t)), seat)
	second := f.orderItem(t, f.order(t, f.buyer(t)), seat)
	ctx := t.Context()

	if err := f.issueTicket(ctx, first); err != nil {
		t.Fatalf("issue first ticket: %v", err)
	}
	if err := f.issueTicket(ctx, second); pgCode(err) != uniqueViolation {
		t.Fatalf("second active ticket: err = %v, want unique violation", err)
	}
	f.exec(t, `UPDATE tickets SET status = 'revoked', revoked_at = now() WHERE order_item_id = $1`, first)
	if err := f.issueTicket(ctx, second); err != nil {
		t.Fatalf("issue ticket after revoke: %v", err)
	}
}

func TestOneSucceededPaymentPerOrder(t *testing.T) {
	db := dbtest.New(t)
	f := newFixture(t, db.Pool)
	order := f.order(t, f.buyer(t))

	insert := `INSERT INTO payments (organizer_id, order_id, status, amount_tiyn, provider)
	           VALUES ($1, $2, $3, 100000, 'sandbox')`
	f.exec(t, insert, f.organizer, order, "failed")
	f.exec(t, insert, f.organizer, order, "succeeded")

	_, err := f.pool.Exec(t.Context(), insert, f.organizer, order, "succeeded")
	if pgCode(err) != uniqueViolation {
		t.Fatalf("second succeeded payment: err = %v, want unique violation", err)
	}
}

func TestTenantIsolation(t *testing.T) {
	db := dbtest.New(t)
	a := newFixture(t, db.Pool)
	b := newFixture(t, db.Pool)

	// Событие организатора B не может ссылаться на зал организатора A.
	_, err := db.Pool.Exec(t.Context(), `
		INSERT INTO events (organizer_id, venue_id, seat_map_id, slug, title, starts_at, ends_at)
		VALUES ($1, $2, $3, 'foreign', 'Foreign', now() + interval '1 day', now() + interval '2 days')`,
		b.organizer, a.venue, b.seatMap)
	if pgCode(err) != foreignKeyViolation {
		t.Fatalf("cross-tenant venue: err = %v, want foreign key violation", err)
	}

	// Заказ организатора B не может содержать место события A.
	orderB := b.order(t, b.buyer(t))
	seatA := a.seat(t)
	_, err = db.Pool.Exec(t.Context(), `
		INSERT INTO order_items (organizer_id, event_id, order_id, event_seat_id, price_tiyn)
		VALUES ($1, $2, $3, $4, 100000)`, b.organizer, b.event, orderB, seatA)
	if pgCode(err) != foreignKeyViolation {
		t.Fatalf("cross-tenant seat: err = %v, want foreign key violation", err)
	}
}

func TestRefundMustBelongToOrderPayment(t *testing.T) {
	db := dbtest.New(t)
	f := newFixture(t, db.Pool)
	orderA := f.order(t, f.buyer(t))
	orderB := f.order(t, f.buyer(t))

	var paymentA string
	f.queryRow(t, &paymentA, `
		INSERT INTO payments (organizer_id, order_id, status, amount_tiyn, provider)
		VALUES ($1, $2, 'succeeded', 100000, 'sandbox') RETURNING id`, f.organizer, orderA)

	_, err := db.Pool.Exec(t.Context(), `
		INSERT INTO refunds (organizer_id, payment_id, order_id, amount_tiyn, reason)
		VALUES ($1, $2, $3, 100000, 'buyer_request')`, f.organizer, paymentA, orderB)
	if pgCode(err) != foreignKeyViolation {
		t.Fatalf("refund for another order's payment: err = %v, want foreign key violation", err)
	}
}

func TestTicketMustMatchOrderItemSeat(t *testing.T) {
	db := dbtest.New(t)
	f := newFixture(t, db.Pool)
	order := f.order(t, f.buyer(t))
	item := f.orderItem(t, order, f.seat(t))
	otherSeat := f.generalSeat(t, "Танцпол", "1")

	// Билет по позиции заказа не может указывать на другое место.
	_, err := db.Pool.Exec(t.Context(), `
		INSERT INTO tickets (organizer_id, event_id, order_id, order_item_id, event_seat_id)
		VALUES ($1, $2, $3, $4, $5)`, f.organizer, f.event, order, item, otherSeat)
	if pgCode(err) != foreignKeyViolation {
		t.Fatalf("ticket for a different seat: err = %v, want foreign key violation", err)
	}
}

func TestGeneralAdmissionSeatsAreUnique(t *testing.T) {
	db := dbtest.New(t)
	f := newFixture(t, db.Pool)

	f.generalSeat(t, "Танцпол", "1")
	_, err := f.insertGeneralSeat(t.Context(), "Танцпол", "1")
	if pgCode(err) != uniqueViolation {
		t.Fatalf("duplicate virtual seat: err = %v, want unique violation", err)
	}
}

func TestCheckConstraints(t *testing.T) {
	db := dbtest.New(t)
	f := newFixture(t, db.Pool)
	order := f.order(t, f.buyer(t))
	ctx := t.Context()

	tests := []struct {
		name string
		sql  string
		args []any
	}{
		{"unknown order status", `UPDATE orders SET status = 'done' WHERE id = $1`, []any{order}},
		{"paid without paid_at", `UPDATE orders SET status = 'paid' WHERE id = $1`, []any{order}},
		{"negative price", `INSERT INTO price_categories (organizer_id, event_id, name, price_tiyn)
		                    VALUES ($1, $2, 'Negative', -1)`, []any{f.organizer, f.event}},
		{"held seat without order", `UPDATE event_seats SET status = 'held' WHERE event_id = $1`, []any{f.event}},
		{"phone not in E.164", `INSERT INTO buyers (phone) VALUES ('87001234567')`, nil},
	}
	f.seat(t) // чтобы UPDATE event_seats затронул строку
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.Pool.Exec(ctx, tt.sql, tt.args...)
			if pgCode(err) != checkViolation {
				t.Fatalf("err = %v, want check violation", err)
			}
		})
	}
}

// fixture — минимальный набор данных одного организатора: зал, схема,
// событие и ценовая категория.
type fixture struct {
	pool          *pgxpool.Pool
	organizer     string
	venue         string
	seatMap       string
	event         string
	priceCategory string
}

func newFixture(t *testing.T, pool *pgxpool.Pool) *fixture {
	t.Helper()
	f := &fixture{pool: pool}
	suffix := strings.ToLower(rand.Text()[:10])

	f.queryRow(t, &f.organizer, `INSERT INTO organizers (name, slug) VALUES ('Org', $1) RETURNING id`, "org-"+suffix)
	f.queryRow(t, &f.venue, `INSERT INTO venues (organizer_id, name) VALUES ($1, 'Hall') RETURNING id`, f.organizer)
	f.queryRow(t, &f.seatMap, `
		INSERT INTO seat_maps (organizer_id, venue_id, name, layout)
		VALUES ($1, $2, 'Main', '{}') RETURNING id`, f.organizer, f.venue)
	f.queryRow(t, &f.event, `
		INSERT INTO events (organizer_id, venue_id, seat_map_id, slug, title, starts_at, ends_at)
		VALUES ($1, $2, $3, 'concert', 'Concert', now() + interval '7 days', now() + interval '7 days 3 hours')
		RETURNING id`, f.organizer, f.venue, f.seatMap)
	f.queryRow(t, &f.priceCategory, `
		INSERT INTO price_categories (organizer_id, event_id, name, price_tiyn)
		VALUES ($1, $2, 'Standard', 800000) RETURNING id`, f.organizer, f.event)
	return f
}

// seat создаёт место «сектор A, ряд 1, место 1».
func (f *fixture) seat(t *testing.T) string {
	t.Helper()
	var id string
	f.queryRow(t, &id, `
		INSERT INTO event_seats (organizer_id, event_id, price_category_id, kind, section, row_label, seat_label)
		VALUES ($1, $2, $3, 'seat', 'A', '1', '1') RETURNING id`,
		f.organizer, f.event, f.priceCategory)
	return id
}

func (f *fixture) generalSeat(t *testing.T, section, seat string) string {
	t.Helper()
	id, err := f.insertGeneralSeat(t.Context(), section, seat)
	if err != nil {
		t.Fatalf("insert general seat: %v", err)
	}
	return id
}

func (f *fixture) insertGeneralSeat(ctx context.Context, section, seat string) (string, error) {
	var id string
	err := f.pool.QueryRow(ctx, `
		INSERT INTO event_seats (organizer_id, event_id, price_category_id, kind, section, seat_label)
		VALUES ($1, $2, $3, 'general', $4, $5) RETURNING id`,
		f.organizer, f.event, f.priceCategory, section, seat).Scan(&id)
	return id, err
}

func (f *fixture) buyer(t *testing.T) string {
	t.Helper()
	var id string
	// Случайный номер из 12 цифр после «+7»: коллизии в тесте исключены.
	digits := make([]byte, 10)
	_, _ = rand.Read(digits)
	var b strings.Builder
	b.WriteString("+77")
	for _, d := range digits {
		fmt.Fprintf(&b, "%d", d%10)
	}
	f.queryRow(t, &id, `INSERT INTO buyers (phone) VALUES ($1) RETURNING id`, b.String())
	return id
}

func (f *fixture) order(t *testing.T, buyer string) string {
	t.Helper()
	var id string
	f.queryRow(t, &id, `
		INSERT INTO orders (organizer_id, event_id, buyer_id, email, total_tiyn, expires_at)
		VALUES ($1, $2, $3, 'buyer@example.com', 800000, $4) RETURNING id`,
		f.organizer, f.event, buyer, time.Now().Add(10*time.Minute))
	return id
}

func (f *fixture) orderItem(t *testing.T, order, seat string) string {
	t.Helper()
	var id string
	f.queryRow(t, &id, `
		INSERT INTO order_items (organizer_id, event_id, order_id, event_seat_id, price_tiyn)
		VALUES ($1, $2, $3, $4, 800000) RETURNING id`, f.organizer, f.event, order, seat)
	return id
}

// issueTicket выпускает билет по позиции заказа.
func (f *fixture) issueTicket(ctx context.Context, orderItem string) error {
	_, err := f.pool.Exec(ctx, `
		INSERT INTO tickets (organizer_id, event_id, order_id, order_item_id, event_seat_id)
		SELECT organizer_id, event_id, order_id, id, event_seat_id FROM order_items WHERE id = $1`,
		orderItem)
	return err
}

func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func (f *fixture) queryRow(t *testing.T, dst any, sql string, args ...any) {
	t.Helper()
	if err := f.pool.QueryRow(t.Context(), sql, args...).Scan(dst); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
}

func pgCode(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code
	}
	return ""
}
