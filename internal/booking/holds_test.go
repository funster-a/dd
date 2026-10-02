package booking

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/funster-a/dd/internal/platform/redis"
)

// Отказ Redis не тормозит продажу (ADR 017): вызов ограничен коротким
// таймаутом, после ошибки фильтр выключается и следующие заказы идут в базу,
// не обращаясь к Redis.
func TestHoldGateFailsFastWhenRedisIsDown(t *testing.T) {
	// Неотвечающий адрес: соединение не устанавливается, а висит.
	dead := redis.NewClient("10.255.255.1:6379", quietLog())
	t.Cleanup(func() { _ = dead.Close() })
	opened := 0
	h := newHoldStore(dead, func(error) { opened++ })

	start := time.Now()
	if _, err := h.claim(t.Context(), []string{"k"}, "o1", "", time.Minute); err == nil {
		t.Fatal("claim against a dead Redis succeeded")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("first claim took %v, want about %v", d, redisCallTimeout)
	}
	start = time.Now()
	if _, err := h.claim(t.Context(), []string{"k"}, "o2", "", time.Minute); !errors.Is(err, redis.ErrGateOpen) {
		t.Fatalf("second claim: err = %v, want ErrGateOpen", err)
	}
	if d := time.Since(start); d > 10*time.Millisecond {
		t.Fatalf("claim with the gate open took %v, want immediate", d)
	}
	if opened != 1 {
		t.Fatalf("gate opened %d times, want 1", opened)
	}
	// Отмена запроса — не отказ Redis: фильтр из-за неё не выключается.
	h2 := newHoldStore(dead, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _ = h2.claim(ctx, []string{"k"}, "o3", "", time.Minute)
	if h2.gate.Open() {
		t.Fatal("a cancelled request disabled the gate")
	}
}

// Заказ при мёртвом Redis оформляется через базу, быстро и корректно.
func TestOrderWithRedisDown(t *testing.T) {
	e := newEnv(t, false, 1, 2, 0)
	dead := redis.NewClient("10.255.255.1:6379", quietLog())
	t.Cleanup(func() { _ = dead.Close() })
	e.svc = NewService(e.db.Pool, dead, quietLog())

	start := time.Now()
	if _, err := e.svc.CreateOrder(t.Context(), e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.CreateOrder(t.Context(), e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now())
	if c, ok := errors.AsType[*ConflictError](err); !ok || c.Code != "seat_taken" {
		t.Fatalf("same seat again: err = %v, want seat_taken from the database", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("two orders with Redis down took %v", d)
	}
	e.checkNoDoubleBooking(t)
}
