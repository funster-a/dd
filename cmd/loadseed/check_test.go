package main

import (
	"context"
	"crypto/rand"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/funster-a/dd/internal/booking"
	"github.com/funster-a/dd/internal/platform/db/dbtest"
	"github.com/funster-a/dd/internal/platform/events"
	"github.com/funster-a/dd/internal/platform/redis"
)

// Проверяльщик главного инварианта сам должен быть проверен: на чистых
// данных он молчит, а каждая подложенная порча роняет его. Интеграционные
// тесты: нужен DATABASE_TEST_URL, для сверки холдов — REDIS_TEST_ADDR.

// scenario — событие на 3×5 мест после настоящих операций booking на момент now:
//
//	A — оплатил места 1-1 и 1-2, за билет на 1-2 деньги вернулись, место снова свободно;
//	B — корзина на 1-3;
//	C — корзина на 2-1 истекла, воркер её закрыл и освободил место;
//	D — корзина на 2-2 истекла, воркер до неё ещё не дошёл;
//	E — после конца срока D забрал место 2-2 в свою корзину;
//	G — корзина на 3-5 истекла, воркер до неё не дошёл, место никто не взял.
//
// Действующие позиции: 1-1 (A), 1-3 (B), 2-2 (E); занятые места — те же три.
// В той же базе есть второе событие с оплаченным местом и корзиной: проверка
// одного события не должна их видеть.
type scenario struct {
	pool          *pgxpool.Pool
	event         string
	now           time.Time
	a, b, d, e    string
	payment       string // успешный платёж A
	organizer     string
	buyerForExtra string
}

func newScenario(t *testing.T) *scenario {
	t.Helper()
	ctx := t.Context()
	sc := &scenario{pool: dbtest.New(t).Pool, now: time.Now()}
	sc.event = sc.seed(t)
	other := sc.seed(t)
	if err := sc.pool.QueryRow(ctx, `SELECT organizer_id FROM events WHERE id = $1`, sc.event).Scan(&sc.organizer); err != nil {
		t.Fatal(err)
	}

	svc := booking.NewService(sc.pool, nil, slog.New(slog.DiscardHandler))
	order := func(event string, at time.Time, seats ...string) booking.Order {
		t.Helper()
		req := booking.OrderRequest{Email: "buyer@example.com"}
		for _, s := range seats {
			req.Seats = append(req.Seats, booking.SeatRef{Section: "Партер", Row: s[:1], Seat: s[2:]})
		}
		o, err := svc.CreateOrder(ctx, sc.buyer(ctx, t), event, req, at)
		if err != nil {
			t.Fatalf("order %v: %v", seats, err)
		}
		return o
	}
	pay := func(o booking.Order) {
		t.Helper()
		if err := svc.ConfirmPayment(ctx, events.PaymentSucceededEvent{
			PaymentID: uuid.NewString(), OrderID: o.ID, AmountTiyn: o.TotalTiyn, PaidAt: sc.now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	past := sc.now.Add(-2 * booking.HoldTTL)

	a := order(sc.event, sc.now, "1-1", "1-2")
	pay(a)
	sc.a = a.ID
	sc.b = order(sc.event, sc.now, "1-3").ID
	order(sc.event, past, "2-1")
	if _, err := svc.ExpireOrders(ctx, sc.now, 100); err != nil {
		t.Fatal(err)
	}
	sc.d = order(sc.event, past, "2-2").ID
	sc.e = order(sc.event, sc.now, "2-2").ID
	order(sc.event, past, "3-5")
	pay(order(other, sc.now, "1-1"))
	order(other, sc.now, "1-2")

	// Частичный возврат так, как его оставляют модули payment, ticket и
	// booking: билеты выпущены, билет на 1-2 аннулирован, возврат за него
	// прошёл успешно, место снова свободно.
	if err := sc.pool.QueryRow(ctx, `INSERT INTO payments (organizer_id, order_id, status, amount_tiyn, provider)
		VALUES ($1, $2, 'succeeded', $3, 'fakepsp') RETURNING id`, sc.organizer, sc.a, a.TotalTiyn).Scan(&sc.payment); err != nil {
		t.Fatal(err)
	}
	sc.exec(t, `INSERT INTO tickets (organizer_id, event_id, order_id, order_item_id, event_seat_id)
		SELECT organizer_id, event_id, order_id, id, event_seat_id FROM order_items WHERE order_id = $1`, sc.a)
	sc.exec(t, `UPDATE tickets SET status = 'revoked', revoked_at = $2 WHERE id = $1`, sc.ticket(t, "1-2"), sc.now)
	sc.refund(t, sc.pool, "1-2", "succeeded")
	sc.exec(t, `UPDATE event_seats SET status = 'available' WHERE id = $1`, sc.seat(t, "1-2"))
	sc.exec(t, `UPDATE orders SET status = 'partially_refunded' WHERE id = $1`, sc.a)
	sc.buyerForExtra = sc.buyer(ctx, t)
	return sc
}

// seed — опубликованное событие на 3×5 мест с открытыми продажами.
func (sc *scenario) seed(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	if err := seedEvent(t.Context(), sc.pool, []string{"-rows", "3", "-seats", "5", "-out", path}); err != nil {
		t.Fatal(err)
	}
	var ev struct {
		EventID string `json:"event_id"`
	}
	if err := readJSON(path, &ev); err != nil {
		t.Fatal(err)
	}
	sc.exec(t, `UPDATE events SET sales_start_at = $2 WHERE id = $1`, ev.EventID, sc.now.Add(-24*time.Hour))
	return ev.EventID
}

// ticket — билет A на место label.
func (sc *scenario) ticket(t *testing.T, label string) string {
	t.Helper()
	var id string
	if err := sc.pool.QueryRow(t.Context(), `SELECT id FROM tickets WHERE order_id = $1 AND event_seat_id = $2`,
		sc.a, sc.seat(t, label)).Scan(&id); err != nil {
		t.Fatalf("ticket %s: %v", label, err)
	}
	return id
}

// refund — возврат за билет A на место label в статусе status.
func (sc *scenario) refund(t *testing.T, db interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, label, status string) {
	t.Helper()
	ticket := sc.ticket(t, label)
	if _, err := db.Exec(t.Context(), `WITH r AS (
		  INSERT INTO refunds (organizer_id, payment_id, order_id, status, amount_tiyn, reason)
		  VALUES ($1, $2, $3, $4, 500000, 'buyer_request') RETURNING id)
		INSERT INTO refund_items (refund_id, ticket_id, organizer_id, order_id)
		SELECT id, $5, $1, $3 FROM r`, sc.organizer, sc.payment, sc.a, status, ticket); err != nil {
		t.Fatalf("refund %s: %v", label, err)
	}
}

func (sc *scenario) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := sc.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (sc *scenario) buyer(ctx context.Context, t *testing.T) string {
	t.Helper()
	n, err := rand.Int(rand.Reader, big.NewInt(9_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	var id string
	phone := "+7" + strconv.FormatInt(1_000_000_000+n.Int64(), 10)
	if err := sc.pool.QueryRow(ctx, `INSERT INTO buyers (phone) VALUES ($1) RETURNING id`, phone).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// seat — id места «ряд-место» в секторе «Партер».
func (sc *scenario) seat(t *testing.T, label string) string {
	t.Helper()
	var id string
	if err := sc.pool.QueryRow(t.Context(), `SELECT id FROM event_seats
		WHERE event_id = $1 AND section = 'Партер' AND row_label = $2 AND seat_label = $3`,
		sc.event, label[:1], label[2:]).Scan(&id); err != nil {
		t.Fatalf("seat %s: %v", label, err)
	}
	return id
}

// cart — ещё одна действующая корзина без позиций.
func (sc *scenario) cart(t *testing.T, tx pgx.Tx) string {
	t.Helper()
	var id string
	if err := tx.QueryRow(t.Context(), `INSERT INTO orders (organizer_id, event_id, buyer_id, email, total_tiyn, expires_at)
		VALUES ($1, $2, $3, 'extra@example.com', 0, $4) RETURNING id`,
		sc.organizer, sc.event, sc.buyerForExtra, sc.now.Add(booking.HoldTTL)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestInvariantCleanAfterRealOperations(t *testing.T) {
	sc := newScenario(t)
	r, err := checkInvariant(t.Context(), sc.pool, sc.event, sc.now)
	if err != nil {
		t.Fatal(err)
	}
	want := invariant{ActiveOrders: 3, OrderItems: 3, HeldOrSold: 3}
	if r != want || r.violated() {
		t.Fatalf("clean data: got %+v, want %+v", r, want)
	}

	// Команда loadseed check: тот же итог в файле и без ошибки.
	out := filepath.Join(t.TempDir(), "check.json")
	if err := check(t.Context(), sc.pool, []string{"-event", sc.event, "-out", out}); err != nil {
		t.Fatalf("check on clean data: %v", err)
	}
	var got invariant
	if err := readJSON(out, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("check.json: got %+v, want %+v", got, want)
	}

	if _, err := checkInvariant(t.Context(), sc.pool, uuid.NewString(), sc.now); err == nil {
		t.Fatal("unknown event must be an error, not an all-zero clean result")
	}
}

// Законные состояния, которые система оставляет и после того, как очередь
// событий разобрана: проверка обязана считать их чистыми.
func TestInvariantAcceptsLegitimateStates(t *testing.T) {
	sc := newScenario(t)
	cases := []struct {
		name  string
		setup func(t *testing.T, tx pgx.Tx)
	}{
		{
			name: "refund rejected: revoked ticket keeps its seat",
			setup: func(t *testing.T, tx pgx.Tx) {
				mustExec(t, tx, `UPDATE tickets SET status = 'revoked', revoked_at = $2 WHERE id = $1`, sc.ticket(t, "1-1"), sc.now)
				sc.refund(t, tx, "1-1", "failed")
			},
		},
		{
			name: "refund in progress: revoked ticket keeps its seat",
			setup: func(t *testing.T, tx pgx.Tx) {
				mustExec(t, tx, `UPDATE tickets SET status = 'revoked', revoked_at = $2 WHERE id = $1`, sc.ticket(t, "1-1"), sc.now)
				sc.refund(t, tx, "1-1", "requested")
			},
		},
		{
			name: "cancelled event releases the seat of a used ticket",
			setup: func(t *testing.T, tx pgx.Tx) {
				mustExec(t, tx, `UPDATE events SET status = 'cancelled', cancelled_at = $2 WHERE id = $1`, sc.event, sc.now)
				mustExec(t, tx, `UPDATE tickets SET status = 'used', used_at = $2 WHERE id = $1`, sc.ticket(t, "1-1"), sc.now)
				mustExec(t, tx, `UPDATE event_seats SET status = 'available' WHERE id = $1`, sc.seat(t, "1-1"))
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := t.Context()
			tx, err := sc.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			c.setup(t, tx)
			r, err := checkInvariant(ctx, tx, sc.event, sc.now)
			if err != nil {
				t.Fatal(err)
			}
			if r.violated() {
				t.Fatalf("legitimate state reported as a violation: %+v", r)
			}
		})
	}
}

func TestInvariantCatchesPlantedViolations(t *testing.T) {
	sc := newScenario(t)
	cases := []struct {
		name  string
		plant func(t *testing.T, tx pgx.Tx)
		// field — какой счётчик обязан заметить порчу.
		field func(invariant) int
		// balanced — порча не меняет общий счёт: её видно только при сверке по местам.
		balanced bool
	}{
		{
			name: "seat in two active orders",
			plant: func(t *testing.T, tx pgx.Tx) {
				f := sc.cart(t, tx)
				mustExec(t, tx, `INSERT INTO order_items (organizer_id, event_id, order_id, event_seat_id, price_tiyn)
					VALUES ($1, $2, $3, $4, 0)`, sc.organizer, sc.event, f, sc.seat(t, "1-1"))
			},
			field: func(r invariant) int { return r.DoubleBooked },
		},
		{
			name: "sold seat without order and lost cart seat",
			plant: func(t *testing.T, tx pgx.Tx) {
				mustExec(t, tx, `UPDATE event_seats SET status = 'sold' WHERE id = $1`, sc.seat(t, "3-1"))
				mustExec(t, tx, `UPDATE event_seats SET status = 'available', hold_order_id = NULL, hold_expires_at = NULL
					WHERE id = $1`, sc.seat(t, "1-3"))
			},
			field:    func(r invariant) int { return r.Orphaned },
			balanced: true,
		},
		{
			name: "sold seat whose only item is an unpaid cart",
			plant: func(t *testing.T, tx pgx.Tx) {
				mustExec(t, tx, `UPDATE event_seats SET status = 'sold', hold_order_id = NULL, hold_expires_at = NULL
					WHERE id = $1`, sc.seat(t, "1-3"))
			},
			field:    func(r invariant) int { return r.Orphaned },
			balanced: true,
		},
		{
			name: "paid seat available again",
			plant: func(t *testing.T, tx pgx.Tx) {
				mustExec(t, tx, `UPDATE event_seats SET status = 'available' WHERE id = $1`, sc.seat(t, "1-1"))
			},
			field: func(r invariant) int { return r.Mismatched },
		},
		{
			name: "hold without its order item",
			plant: func(t *testing.T, tx pgx.Tx) {
				mustExec(t, tx, `UPDATE event_seats SET status = 'held', hold_order_id = $2, hold_expires_at = $3
					WHERE id = $1`, sc.seat(t, "3-2"), sc.b, sc.now.Add(booking.HoldTTL))
			},
			field: func(r invariant) int { return r.Orphaned },
		},
		{
			name: "cart seat held by another order",
			plant: func(t *testing.T, tx pgx.Tx) {
				mustExec(t, tx, `UPDATE event_seats SET hold_order_id = $2 WHERE id = $1`, sc.seat(t, "1-3"), sc.e)
			},
			field:    func(r invariant) int { return r.Mismatched },
			balanced: true,
		},
		{
			name: "money returned but seat still sold",
			plant: func(t *testing.T, tx pgx.Tx) {
				mustExec(t, tx, `UPDATE event_seats SET status = 'sold' WHERE id = $1`, sc.seat(t, "1-2"))
			},
			field: func(r invariant) int { return r.Orphaned },
		},
		{
			name: "double booking in a cancelled event",
			plant: func(t *testing.T, tx pgx.Tx) {
				mustExec(t, tx, `UPDATE events SET status = 'cancelled', cancelled_at = $2 WHERE id = $1`, sc.event, sc.now)
				f := sc.cart(t, tx)
				mustExec(t, tx, `INSERT INTO order_items (organizer_id, event_id, order_id, event_seat_id, price_tiyn)
					VALUES ($1, $2, $3, $4, 0)`, sc.organizer, sc.event, f, sc.seat(t, "1-1"))
			},
			field: func(r invariant) int { return r.DoubleBooked },
		},
		{
			name: "expired cart revived without its hold",
			plant: func(t *testing.T, tx pgx.Tx) {
				mustExec(t, tx, `UPDATE orders SET expires_at = $2 WHERE id = $1`, sc.d, sc.now.Add(booking.HoldTTL))
			},
			field: func(r invariant) int { return r.Mismatched },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := t.Context()
			tx, err := sc.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			c.plant(t, tx)
			r, err := checkInvariant(ctx, tx, sc.event, sc.now)
			if err != nil {
				t.Fatal(err)
			}
			if !r.violated() || c.field(r) == 0 {
				t.Fatalf("violation not caught: %+v", r)
			}
			if c.balanced && r.HeldOrSold != r.OrderItems {
				t.Fatalf("case must keep totals equal to show that the per-seat check catches it: %+v", r)
			}
		})
	}
}

func mustExec(t *testing.T, tx pgx.Tx, sql string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestCompareHolds(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR is not set")
	}
	sc := newScenario(t)
	ctx := t.Context()
	rdb := redis.NewClient(addr, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = rdb.Close() })

	// Холд D в базе со вышедшим сроком: ключа в Redis у него законно нет.
	sc.exec(t, `UPDATE event_seats SET status = 'held', hold_order_id = $2, hold_expires_at = $3 WHERE id = $1`,
		sc.seat(t, "3-4"), sc.d, sc.now.Add(-time.Minute))
	key := func(label string) string { return holdKey(sc.event, "Партер", label[:1], label[2:]) }
	planted := map[string]string{
		key("2-2"): sc.d, // место держит E, а ключ — на D
		key("3-3"): sc.b, // место свободно
		key("1-1"): sc.a, // место продано: ключ после оплаты не мешает
	}
	for k, v := range planted {
		if err := rdb.Set(ctx, k, v, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rdb.Del(context.Background(), k) })
	}
	// Ключа холда B на 1-3 нет: место держится в базе, фильтр его не видит.

	r, err := compareHolds(ctx, sc.pool, rdb, sc.event, sc.now)
	if err != nil {
		t.Fatal(err)
	}
	want := holds{RedisHolds: 3, DBHeld: 2, GhostFree: 1, GhostOther: 1, Missing: 1}
	if r != want {
		t.Fatalf("got %+v, want %+v", r, want)
	}
}
