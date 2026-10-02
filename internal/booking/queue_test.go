package booking

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/funster-a/dd/internal/platform/redis"
)

func TestQueueMath(t *testing.T) {
	w := &waitingRoom{cfg: QueueConfig{AdmitPerSecond: 10, OpensBefore: 15 * time.Minute, Window: 2 * time.Hour}}
	start := time.Date(2027, 6, 1, 10, 0, 0, 0, time.UTC)

	for _, tt := range []struct {
		at   time.Time
		want phase
	}{
		{start.Add(-16 * time.Minute), phaseNotOpen},
		{start.Add(-15 * time.Minute), phaseOpen},
		{start.Add(time.Hour), phaseOpen},
		{start.Add(2 * time.Hour), phaseNone},
	} {
		if got := w.phase(&start, tt.at); got != tt.want {
			t.Errorf("phase at %v = %v, want %v", tt.at.Sub(start), got, tt.want)
		}
	}
	if w.phase(nil, start) != phaseNone {
		t.Error("an event without a sales start must have no queue")
	}
	var off *waitingRoom
	if off.phase(&start, start) != phaseNone {
		t.Error("a disabled queue must have no phase")
	}

	if n := w.admittedCount(start, start.Add(-time.Second)); n != 0 {
		t.Errorf("admitted before start = %d", n)
	}
	if n := w.admittedCount(start, start.Add(2500*time.Millisecond)); n != 25 {
		t.Errorf("admitted after 2.5 s at 10/s = %d, want 25", n)
	}

	// 25 пропущены; место 30 (с 0) — шестое среди ждущих, пропуск через
	// (30+1)/10 = 3,1 с после старта, то есть через 0,6 с.
	st := w.status(start, 30, start.Add(2500*time.Millisecond))
	if st.State != "waiting" || st.Position != 6 || st.EstimatedWaitSeconds != 1 {
		t.Errorf("status = %+v", st)
	}
	if st := w.status(start, 24, start.Add(2500*time.Millisecond)); st.State != "admitted" {
		t.Errorf("rank 24 = %+v, want admitted", st)
	}
	// До старта ждут все, оценка — от старта.
	if st := w.status(start, 99, start.Add(-time.Minute)); st.State != "waiting" || st.Position != 100 || st.EstimatedWaitSeconds != 70 {
		t.Errorf("status before start = %+v", st)
	}

	if s := score(start, start.Add(-time.Minute)); s < 0 || s >= 1 {
		t.Errorf("score before start = %v, want [0, 1)", s)
	}
	if s := score(start, start.Add(3*time.Second)); s != 4 {
		t.Errorf("score 3 s after start = %v, want 4", s)
	}
}

// queueEnv — событие со стартом продаж через час и сервис с очередью,
// которая пропускает rate покупателей в секунду.
func queueEnv(t *testing.T, rate int) (*env, time.Time) {
	t.Helper()
	e := newEnv(t, true, 10, 10, 0)
	e.svc = NewService(e.db.Pool, e.rdb, quietLog(), WithQueue(DefaultQueueConfig(rate)))
	start := time.Now().Add(time.Hour).Truncate(time.Second)
	e.exec(t, `UPDATE events SET sales_start_at = $1 WHERE id = $2`, start, e.eventID)
	return e, start
}

func TestQueue(t *testing.T) {
	e, start := queueEnv(t, 2)
	ctx := t.Context()

	if st, err := e.svc.JoinQueue(ctx, e.buyer(t), e.eventID, start.Add(-20*time.Minute)); err != nil || st.State != "not_open" || st.OpensAt == nil {
		t.Fatalf("join 20 min before start = %+v, %v", st, err)
	}
	if a := e.availability(t); a.Queue == nil || !a.Queue.OpensAt.Equal(start.Add(-15*time.Minute)) {
		t.Errorf("availability queue = %+v", a.Queue)
	}

	// Пятеро встали до старта: номера — жребий, но все разные. Номер
	// пришедшего до старта может оказаться впереди уже стоящих, поэтому
	// места проверяются, когда встали все.
	early := start.Add(-time.Minute)
	buyers := make([]string, 5)
	for i := range buyers {
		buyers[i] = e.buyer(t)
		if st, err := e.svc.JoinQueue(ctx, buyers[i], e.eventID, early); err != nil || st.State != "waiting" {
			t.Fatalf("join = %+v, %v", st, err)
		}
	}
	seen := map[int64]bool{}
	for _, b := range buyers {
		st, _ := e.svc.JoinQueue(ctx, b, e.eventID, early)
		seen[st.Position] = true
	}
	if len(seen) != 5 || !seen[1] || !seen[5] {
		t.Errorf("positions = %v, want 1..5", seen)
	}
	// Повторный вызов место не меняет.
	first, _ := e.svc.JoinQueue(ctx, buyers[0], e.eventID, early)
	again, _ := e.svc.JoinQueue(ctx, buyers[0], e.eventID, early.Add(10*time.Second))
	if first.Position != again.Position {
		t.Errorf("position changed on re-join: %d → %d", first.Position, again.Position)
	}

	// Через секунду после старта пропущены двое.
	at := start.Add(time.Second)
	var admitted, waiting []string
	for _, b := range buyers {
		st, _ := e.svc.JoinQueue(ctx, b, e.eventID, at)
		switch st.State {
		case "admitted":
			admitted = append(admitted, b)
		case "waiting":
			waiting = append(waiting, b)
		}
	}
	if len(admitted) != 2 || len(waiting) != 3 {
		t.Fatalf("after 1 s at 2/s: admitted %d, waiting %d", len(admitted), len(waiting))
	}

	// Заказ — только дождавшимся.
	if _, err := e.svc.CreateOrder(ctx, waiting[0], e.eventID, seatsReq(seat(1, 1)), at); !isPrecondition(err, "queue_required") {
		t.Errorf("order of a waiting buyer: %s", fmtErr(err))
	}
	if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 2)), at); !isPrecondition(err, "queue_required") {
		t.Errorf("order of a buyer outside the queue: %s", fmtErr(err))
	}
	if _, err := e.svc.CreateOrder(ctx, admitted[0], e.eventID, seatsReq(seat(1, 3)), at); err != nil {
		t.Errorf("order of an admitted buyer: %s", fmtErr(err))
	}

	// Пришедший после старта — позади всех пришедших до старта.
	late := e.buyer(t)
	if st, _ := e.svc.JoinQueue(ctx, late, e.eventID, at); st.State != "waiting" || st.Position != 4 {
		t.Errorf("late buyer = %+v, want 4th in line", st)
	}
	if st, _ := e.svc.JoinQueue(ctx, late, e.eventID, start.Add(3*time.Second)); st.State != "admitted" {
		t.Errorf("late buyer after the queue caught up = %+v", st)
	}

	// После окна очереди покупают без неё.
	after := start.Add(2*time.Hour + time.Second)
	outsider := e.buyer(t)
	if st, _ := e.svc.JoinQueue(ctx, outsider, e.eventID, after); st.State != "not_required" {
		t.Errorf("join after the window = %+v", st)
	}
	if _, err := e.svc.CreateOrder(ctx, outsider, e.eventID, seatsReq(seat(2, 1)), after); err != nil {
		t.Errorf("order after the window: %s", fmtErr(err))
	}
	e.checkNoDoubleBooking(t)
}

// Очередь пропускает к покупке ровно столько покупателей, сколько положено
// к этому моменту, при любой конкурентности.
func TestQueueConcurrent(t *testing.T) {
	e, start := queueEnv(t, 5)
	ctx := t.Context()
	const n = 60
	buyers := make([]string, n)
	for i := range buyers {
		buyers[i] = e.buyer(t)
	}
	var wg sync.WaitGroup
	for _, b := range buyers {
		wg.Go(func() {
			if _, err := e.svc.JoinQueue(ctx, b, e.eventID, start.Add(-time.Minute)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	at := start.Add(4 * time.Second) // пропущено 20 из 60
	var ok, queued atomic.Int32
	for i, b := range buyers {
		wg.Go(func() {
			_, err := e.svc.CreateOrder(ctx, b, e.eventID, seatsReq(seat(i/10+1, i%10+1)), at)
			switch {
			case err == nil:
				ok.Add(1)
			case isPrecondition(err, "queue_required"):
				queued.Add(1)
			default:
				t.Errorf("order: %s", fmtErr(err))
			}
		})
	}
	wg.Wait()
	if ok.Load() != 20 || queued.Load() != n-20 {
		t.Errorf("orders: %d ok, %d sent back to the queue; want 20 and %d", ok.Load(), queued.Load(), n-20)
	}
	e.checkNoDoubleBooking(t)
}

// Очередь — сглаживание нагрузки, а не корректность: без Redis покупка
// идёт напрямую, а не останавливается.
func TestQueueWithoutRedis(t *testing.T) {
	e, start := queueEnv(t, 1)
	dead := redis.NewClient("127.0.0.1:1", quietLog())
	t.Cleanup(func() { _ = dead.Close() })
	e.svc = NewService(e.db.Pool, dead, quietLog(), WithQueue(DefaultQueueConfig(1)))
	ctx := t.Context()
	at := start.Add(time.Second)

	buyer := e.buyer(t)
	if st, err := e.svc.JoinQueue(ctx, buyer, e.eventID, at); err != nil || st.State != "not_required" {
		t.Errorf("join without redis = %+v, %v", st, err)
	}
	if _, err := e.svc.CreateOrder(ctx, buyer, e.eventID, seatsReq(seat(1, 1)), at); err != nil {
		t.Errorf("order without redis: %s", fmtErr(err))
	}
}

func isPrecondition(err error, code string) bool {
	p, ok := errors.AsType[*PreconditionError](err)
	return ok && p.Code == code
}
