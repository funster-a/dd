package booking

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/funster-a/dd/internal/platform/db/dbtest"
	"github.com/funster-a/dd/internal/platform/redis"
)

// Интеграционные тесты: нужны DATABASE_TEST_URL и, для режима с Redis,
// REDIS_TEST_ADDR. Модуль booking не импортирует catalog, поэтому
// опубликованное событие с местами создаётся прямо SQL.

const (
	partPrice int64 = 500_000 // 5 000 ₸ в тиынах
	fanPrice  int64 = 200_000
)

type env struct {
	svc     *Service
	db      *dbtest.DB
	rdb     *goredis.Client // nil в режиме «только база»
	eventID string
}

// modes — режимы захвата: с фильтром в Redis и только база (Redis
// недоступен). Главный инвариант обязан держаться в обоих.
func modes() map[string]bool {
	m := map[string]bool{"database only": false}
	if os.Getenv("REDIS_TEST_ADDR") != "" {
		m["with redis"] = true
	}
	return m
}

func quietLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// newEnv — событие с сектором «Партер» rows×seats мест с рядом и входной
// зоной «Фан-зона» на general виртуальных мест.
func newEnv(t *testing.T, withRedis bool, rows, seats, general int) *env {
	t.Helper()
	d := dbtest.New(t)
	e := &env{db: d}
	var scripter goredis.Scripter
	if withRedis {
		addr := os.Getenv("REDIS_TEST_ADDR")
		if addr == "" {
			t.Skip("REDIS_TEST_ADDR is not set")
		}
		e.rdb = redis.NewClient(addr, quietLog())
		t.Cleanup(func() { _ = e.rdb.Close() })
		scripter = e.rdb
	}
	e.svc = NewService(d.Pool, scripter, quietLog())
	e.eventID = e.publishEvent(t, "published", rows, seats, general)
	return e
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.db.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) publishEvent(t *testing.T, status string, rows, seats, general int) string {
	t.Helper()
	ctx := t.Context()
	var org, venue, seatMap, event, part, fan string
	slug := "org-" + strings.ToLower(rand.Text()[:8])
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	p := e.db.Pool
	must(p.QueryRow(ctx, `INSERT INTO organizers (name, slug) VALUES ('Org', $1) RETURNING id`, slug).Scan(&org))
	must(p.QueryRow(ctx, `INSERT INTO venues (organizer_id, name) VALUES ($1, 'Hall') RETURNING id`, org).Scan(&venue))
	must(p.QueryRow(ctx, `INSERT INTO seat_maps (organizer_id, venue_id, name, layout) VALUES ($1, $2, 'Main', '{}') RETURNING id`,
		org, venue).Scan(&seatMap))
	start := time.Now().Add(30 * 24 * time.Hour)
	publishedAt := any(time.Now())
	if status == "draft" {
		publishedAt = nil
	}
	must(p.QueryRow(ctx, `INSERT INTO events (organizer_id, venue_id, seat_map_id, slug, title, status, starts_at, ends_at, published_at, max_tickets_per_buyer)
		VALUES ($1, $2, $3, 'show', 'Show', $4, $5, $6, $7, 10) RETURNING id`,
		org, venue, seatMap, status, start, start.Add(2*time.Hour), publishedAt).Scan(&event))
	must(p.QueryRow(ctx, `INSERT INTO price_categories (organizer_id, event_id, name, price_tiyn) VALUES ($1, $2, 'Партер', $3) RETURNING id`,
		org, event, partPrice).Scan(&part))
	must(p.QueryRow(ctx, `INSERT INTO price_categories (organizer_id, event_id, name, price_tiyn) VALUES ($1, $2, 'Фан', $3) RETURNING id`,
		org, event, fanPrice).Scan(&fan))
	e.exec(t, `INSERT INTO event_seats (organizer_id, event_id, price_category_id, kind, section, row_label, seat_label)
		SELECT $1, $2, $3, 'seat', 'Партер', r::text, s::text FROM generate_series(1, $4) r, generate_series(1, $5) s`,
		org, event, part, rows, seats)
	e.exec(t, `INSERT INTO event_seats (organizer_id, event_id, price_category_id, kind, section, row_label, seat_label)
		SELECT $1, $2, $3, 'general', 'Фан-зона', NULL, n::text FROM generate_series(1, $4) n`,
		org, event, fan, general)
	return event
}

func (e *env) buyer(t *testing.T) string {
	t.Helper()
	n, err := rand.Int(rand.Reader, big.NewInt(9_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	var id string
	phone := "+7" + strconv.FormatInt(1_000_000_000+n.Int64(), 10)
	if err := e.db.Pool.QueryRow(t.Context(), `INSERT INTO buyers (phone) VALUES ($1) RETURNING id`, phone).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seat(row, n int) SeatRef {
	return SeatRef{Section: "Партер", Row: strconv.Itoa(row), Seat: strconv.Itoa(n)}
}

func seatsReq(refs ...SeatRef) OrderRequest {
	return OrderRequest{Seats: refs, Email: "buyer@example.com"}
}

func generalReq(n int) OrderRequest {
	return OrderRequest{General: []GeneralRef{{Section: "Фан-зона", Quantity: n}}, Email: "buyer@example.com"}
}

// seatStatus — статус места и заказ, который его держит.
func (e *env) seatStatus(t *testing.T, r SeatRef) (status string, holder *string) {
	t.Helper()
	err := e.db.Pool.QueryRow(t.Context(), `SELECT status, hold_order_id FROM event_seats
		WHERE event_id = $1 AND section = $2 AND row_label = $3 AND seat_label = $4`,
		e.eventID, r.Section, r.Row, r.Seat).Scan(&status, &holder)
	if err != nil {
		t.Fatal(err)
	}
	return status, holder
}

// checkNoDoubleBooking — главный инвариант на уровне данных: ни одно место
// не входит в два действующих заказа, и каждое удерживаемое место держит
// ровно тот заказ, в позициях которого оно есть.
func (e *env) checkNoDoubleBooking(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	var dup int
	if err := e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM (
		SELECT i.event_seat_id FROM order_items i JOIN orders o ON o.id = i.order_id
		WHERE o.event_id = $1 AND o.status = 'pending'
		GROUP BY 1 HAVING count(*) > 1) d`, e.eventID).Scan(&dup); err != nil {
		t.Fatal(err)
	}
	if dup != 0 {
		t.Fatalf("%d seats are in more than one pending order", dup)
	}
	var mismatch int
	if err := e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM order_items i
		JOIN orders o ON o.id = i.order_id
		JOIN event_seats s ON s.id = i.event_seat_id
		WHERE o.event_id = $1 AND o.status = 'pending'
		  AND (s.status <> 'held' OR s.hold_order_id <> o.id)`, e.eventID).Scan(&mismatch); err != nil {
		t.Fatal(err)
	}
	if mismatch != 0 {
		t.Fatalf("%d pending order items are not held by their order", mismatch)
	}
}

func fmtErr(err error) string { return fmt.Sprintf("%T %v", err, err) }
