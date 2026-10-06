package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/funster-a/dd/internal/booking"
	"github.com/funster-a/dd/internal/platform/events"
	"github.com/funster-a/dd/internal/platform/outbox"
)

// Поток оплат для отказа RabbitMQ (ADR 030). Путь от оплаты до билета идёт
// через очередь: payment.succeeded → booking → order.paid → ticket.
//
// paystream создаёт n заказов по одному месту и пишет в outbox события
// payment.succeeded со скоростью rate в секунду — то, что пишет модуль оплаты,
// получив вебхук провайдера. Дальше путь настоящий: релей воркера, RabbitMQ,
// бронирование, снова outbox и очередь, выпуск билетов.
//
// tickets-check после прогона считает, сколько оплаченных заказов получили
// билеты и через сколько после оплаты.

type streamFile struct {
	EventID string    `json:"event_id"`
	Orders  []string  `json:"orders"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
}

func payStream(ctx context.Context, pool *pgxpool.Pool, args []string) error {
	fs := flag.NewFlagSet("paystream", flag.ContinueOnError)
	eventFile := fs.String("event", "event.json", "событие от loadseed event")
	n := fs.Int("n", 6000, "сколько заказов оплатить")
	rate := fs.Int("rate", 100, "оплат в секунду")
	out := fs.String("out", "stream.json", "куда записать заказы и время потока")
	started := fs.String("started", "", "файл, который создаётся в момент начала оплат: по нему серия отсчитывает отказ")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var ev struct {
		EventID     string `json:"event_id"`
		Rows        int    `json:"rows"`
		SeatsPerRow int    `json:"seats_per_row"`
	}
	b, err := os.ReadFile(*eventFile) //nolint:gosec // путь задаёт автор прогона
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &ev); err != nil {
		return err
	}
	if *n > ev.Rows*ev.SeatsPerRow {
		return fmt.Errorf("event has %d seats, want %d", ev.Rows*ev.SeatsPerRow, *n)
	}

	buyers, err := insertBuyers(ctx, pool, *n)
	if err != nil {
		return err
	}
	type order struct {
		id    string
		total int64
	}
	orders := make([]order, *n)
	book := booking.NewService(pool, nil, slog.New(slog.DiscardHandler))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	next := make(chan int)
	for range 16 {
		wg.Go(func() {
			for i := range next {
				o, err := book.CreateOrder(ctx, buyers[i], ev.EventID, booking.OrderRequest{
					Seats: []booking.SeatRef{{Section: "Партер", Row: strconv.Itoa(i/ev.SeatsPerRow + 1), Seat: strconv.Itoa(i%ev.SeatsPerRow + 1)}},
					Email: "load@example.com",
				}, time.Now())
				if err != nil {
					mu.Lock()
					firstErr = errors.Join(firstErr, err)
					mu.Unlock()
					continue
				}
				orders[i] = order{o.ID, o.TotalTiyn}
			}
		})
	}
	for i := range *n {
		next <- i
	}
	close(next)
	wg.Wait()
	if firstErr != nil {
		return fmt.Errorf("create orders: %w", firstErr)
	}

	// Оплаты с постоянной скоростью: событие в outbox — как в транзакции
	// вебхука модуля оплаты.
	res := streamFile{EventID: ev.EventID, Start: time.Now().UTC()}
	if *started != "" {
		if err := os.WriteFile(*started, []byte(res.Start.Format(time.RFC3339Nano)), 0o600); err != nil {
			return err
		}
	}
	tick := time.NewTicker(time.Second / time.Duration(*rate))
	defer tick.Stop()
	for _, o := range orders {
		<-tick.C
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			return outbox.Add(ctx, tx, events.PaymentSucceeded, events.PaymentSucceededEvent{
				PaymentID: uuid.Must(uuid.NewV7()).String(), OrderID: o.id, AmountTiyn: o.total, PaidAt: time.Now().UTC(),
			})
		})
		if err != nil {
			return fmt.Errorf("add payment event: %w", err)
		}
		res.Orders = append(res.Orders, o.id)
	}
	res.End = time.Now().UTC()
	return writeJSON(*out, res)
}

// insertBuyers создаёт n покупателей с телефонами из диапазона +7798…,
// которого нет у операторов, и возвращает их id.
func insertBuyers(ctx context.Context, pool *pgxpool.Pool, n int) ([]string, error) {
	base, err := rand.Int(rand.Reader, big.NewInt(9_000_000))
	if err != nil {
		return nil, err
	}
	phones := make([]string, n)
	for i := range phones {
		phones[i] = fmt.Sprintf("+7798%07d", (base.Int64()+int64(i))%10_000_000)
	}
	rows, err := pool.Query(ctx, `INSERT INTO buyers (phone) SELECT unnest($1::text[])
		ON CONFLICT (phone) DO UPDATE SET phone = excluded.phone RETURNING id, phone`, phones)
	if err != nil {
		return nil, err
	}
	byPhone := map[string]string{}
	for rows.Next() {
		var id, phone string
		if err := rows.Scan(&id, &phone); err != nil {
			return nil, err
		}
		byPhone[phone] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, n)
	for i, p := range phones {
		ids[i] = byPhone[p]
	}
	return ids, nil
}

// ticketsCheck ждёт, пока билеты выпущены по всем заказам потока или пока не
// выйдет время, и считает, через сколько после оплаты (события в outbox)
// появился билет.
func ticketsCheck(ctx context.Context, pool *pgxpool.Pool, args []string) error {
	fs := flag.NewFlagSet("tickets-check", flag.ContinueOnError)
	streamPath := fs.String("stream", "stream.json", "заказы от paystream")
	wait := fs.Duration("wait", 2*time.Minute, "сколько ждать недостающие билеты")
	out := fs.String("out", "-", "куда записать итог")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var st streamFile
	b, err := os.ReadFile(*streamPath) //nolint:gosec // путь задаёт автор прогона
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	count := func() (int, error) {
		var c int
		err := pool.QueryRow(ctx, `SELECT count(DISTINCT order_id) FROM tickets WHERE order_id = ANY($1::uuid[])`, st.Orders).Scan(&c)
		return c, err
	}
	deadline := time.Now().Add(*wait)
	issued, err := count()
	for err == nil && issued < len(st.Orders) && time.Now().Before(deadline) {
		time.Sleep(time.Second)
		issued, err = count()
	}
	if err != nil {
		return err
	}

	// Задержка: от записи события оплаты до первого билета заказа, по
	// секундам от старта потока — для графика.
	rows, err := pool.Query(ctx, `
		SELECT extract(epoch FROM o.created_at - $2::timestamptz), extract(epoch FROM min(t.issued_at) - o.created_at)
		FROM outbox o JOIN tickets t ON t.order_id = (o.payload->>'order_id')::uuid
		WHERE o.topic = $3 AND (o.payload->>'order_id')::uuid = ANY($1::uuid[])
		GROUP BY o.id, o.created_at`, st.Orders, st.Start, events.PaymentSucceeded)
	if err != nil {
		return err
	}
	var lat []float64
	bySec := map[int][]float64{}
	for rows.Next() {
		var at, l float64
		if err := rows.Scan(&at, &l); err != nil {
			return err
		}
		lat = append(lat, l)
		bySec[int(at)] = append(bySec[int(at)], l)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	slices.Sort(lat)
	pct := func(p float64) float64 {
		if len(lat) == 0 {
			return 0
		}
		return round3(lat[min(len(lat)-1, int(p*float64(len(lat))))])
	}
	maxBySec := map[string]float64{}
	for s, ls := range bySec {
		maxBySec[strconv.Itoa(s)] = round3(slices.Max(ls))
	}
	var paid int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE id = ANY($1::uuid[]) AND paid_at IS NOT NULL`, st.Orders).Scan(&paid); err != nil {
		return err
	}
	return writeJSON(*out, map[string]any{
		"orders": len(st.Orders), "paid": paid, "with_tickets": issued, "missing": len(st.Orders) - issued,
		"latency_s":        map[string]float64{"p50": pct(0.5), "p95": pct(0.95), "p99": pct(0.99), "max": pct(1)},
		"max_latency_by_s": maxBySec,
	})
}

func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }
