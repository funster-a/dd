package booking

import (
	"errors"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// strategies — стратегии захвата для прогона; redis — только с Redis.
func strategies() []Strategy {
	s := []Strategy{StrategyPessimistic, StrategyOptimistic}
	if os.Getenv("REDIS_TEST_ADDR") != "" {
		s = append(s, StrategyRedis)
	}
	return s
}

func newStrategyEnv(t *testing.T, st Strategy, rows, seats int) *env {
	t.Helper()
	e := newEnv(t, st == StrategyRedis, rows, seats, 0)
	scripter := e.svc.holds
	e.svc = NewService(e.db.Pool, nil, quietLog(), WithStrategy(st))
	if scripter != nil {
		e.svc.holds = scripter
	}
	return e
}

func TestParseStrategy(t *testing.T) {
	for in, want := range map[string]Strategy{"": StrategyRedis, "redis": StrategyRedis, "pessimistic": StrategyPessimistic, "optimistic": StrategyOptimistic} {
		if got, err := ParseStrategy(in); err != nil || got != want {
			t.Errorf("ParseStrategy(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseStrategy("magic"); err == nil {
		t.Error("unknown strategy accepted")
	}
}

// Главный инвариант для каждой стратегии эксперимента: 200 покупателей на
// одно место — ровно одна бронь.
func TestStrategiesSameSeat(t *testing.T) {
	for _, st := range strategies() {
		t.Run(string(st), func(t *testing.T) {
			e := newStrategyEnv(t, st, 1, 1)
			const n = 200
			buyers := make([]string, n)
			for i := range buyers {
				buyers[i] = e.buyer(t)
			}
			var ok, taken atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for _, b := range buyers {
				wg.Go(func() {
					<-start
					_, err := e.svc.CreateOrder(t.Context(), b, e.eventID, seatsReq(seat(1, 1)), time.Now())
					if c, isConflict := errors.AsType[*ConflictError](err); isConflict && c.Code == "seat_taken" {
						taken.Add(1)
						return
					}
					if err != nil {
						t.Error(err)
						return
					}
					ok.Add(1)
				})
			}
			close(start)
			wg.Wait()
			if ok.Load() != 1 || taken.Load() != n-1 {
				t.Fatalf("successes = %d, conflicts = %d; want 1 and %d", ok.Load(), taken.Load(), n-1)
			}
			e.checkNoDoubleBooking(t)
		})
	}
}

// Пересекающиеся наборы мест: ни одно место не попадает в два заказа, у
// пессимистичной стратегии нет взаимоблокировок, у оптимистичной — потерь.
func TestStrategiesOverlappingSeats(t *testing.T) {
	for _, st := range strategies() {
		t.Run(string(st), func(t *testing.T) {
			e := newStrategyEnv(t, st, 3, 4)
			const n = 60
			var wg sync.WaitGroup
			start := make(chan struct{})
			var won atomic.Int32
			for range n {
				b := e.buyer(t)
				picks := rand.Perm(12)[:3] //nolint:gosec // случайный выбор мест в тесте
				refs := make([]SeatRef, len(picks))
				for i, p := range picks {
					refs[i] = seat(p/4+1, p%4+1)
				}
				wg.Go(func() {
					<-start
					_, err := e.svc.CreateOrder(t.Context(), b, e.eventID, seatsReq(refs...), time.Now())
					if _, isConflict := errors.AsType[*ConflictError](err); err != nil && !isConflict {
						t.Error(err)
					}
					if err == nil {
						won.Add(1)
					}
				})
			}
			close(start)
			wg.Wait()
			if won.Load() == 0 || won.Load() > 4 {
				t.Errorf("successful orders = %d, want 1..4", won.Load())
			}
			e.checkNoDoubleBooking(t)
		})
	}
}

// Версию строки ведёт триггер: любое изменение места её увеличивает.
func TestSeatVersionTrigger(t *testing.T) {
	e := newEnv(t, false, 1, 1, 0)
	var before, after int32
	q := `SELECT version FROM event_seats WHERE event_id = $1 AND kind = 'seat'`
	if err := e.db.Pool.QueryRow(t.Context(), q, e.eventID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE event_seats SET hold_expires_at = NULL WHERE event_id = $1`, e.eventID)
	if err := e.db.Pool.QueryRow(t.Context(), q, e.eventID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("version %d → %d, want +1", before, after)
	}
}

// Оптимистичная стратегия: версия поменялась между чтением и записью —
// попытка повторяется и проходит, место по-прежнему свободно.
func TestOptimisticRetriesOnVersionConflict(t *testing.T) {
	e := newStrategyEnv(t, StrategyOptimistic, 1, 1)
	ctx := t.Context()
	retries := func() float64 {
		var m dto.Metric
		if err := defaultMetrics.retries.WithLabelValues(string(StrategyOptimistic)).Write(&m); err != nil {
			t.Fatal(err)
		}
		return m.GetCounter().GetValue()
	}
	was := retries()

	// Чужая транзакция меняет строку места и держит блокировку.
	tx, err := e.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE event_seats SET hold_expires_at = NULL WHERE event_id = $1 AND kind = 'seat'`, e.eventID); err != nil {
		t.Fatal(err)
	}
	buyer := e.buyer(t)
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.CreateOrder(ctx, buyer, e.eventID, seatsReq(seat(1, 1)), time.Now())
		done <- err
	}()
	// Заказ прочитал старую версию и ждёт блокировку на UPDATE.
	time.Sleep(300 * time.Millisecond)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("order after a version conflict: %v", err)
	}
	if got := retries() - was; got < 1 {
		t.Fatalf("retries = %v, want at least 1", got)
	}
	e.checkNoDoubleBooking(t)
}

// Истёкший холд оптимистичная стратегия считает свободным, как и остальные.
func TestOptimisticTakesExpiredHold(t *testing.T) {
	e := newStrategyEnv(t, StrategyOptimistic, 1, 1)
	if _, err := e.svc.CreateOrder(t.Context(), e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now().Add(-HoldTTL-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateOrder(t.Context(), e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now()); err != nil {
		t.Fatalf("expired hold must be free: %v", err)
	}
}
