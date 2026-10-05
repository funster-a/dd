package booking

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/funster-a/dd/internal/platform/idempotency"
)

func TestOrderLifecycle(t *testing.T) {
	for name, withRedis := range modes() {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withRedis, 3, 4, 20)
			ctx := t.Context()
			buyer := e.buyer(t)
			now := time.Now()

			req := seatsReq(seat(1, 1), seat(1, 2))
			req.General = []GeneralRef{{Section: "Фан-зона", Quantity: 3}}
			o, err := e.svc.CreateOrder(ctx, buyer, e.eventID, req, now)
			if err != nil {
				t.Fatal(err)
			}
			if o.Status != "pending" || len(o.Items) != 5 || o.TotalTiyn != 2*partPrice+3*fanPrice || o.Currency != "KZT" {
				t.Fatalf("order = %+v", o)
			}
			if !o.ExpiresAt.Equal(now.Add(HoldTTL).Truncate(time.Microsecond)) && o.ExpiresAt.Sub(now) != HoldTTL {
				t.Errorf("expires_at = %v, want now + %v", o.ExpiresAt, HoldTTL)
			}
			if st, holder := e.seatStatus(t, seat(1, 1)); st != "held" || holder == nil || *holder != o.ID {
				t.Errorf("seat 1-1 = %s held by %v, want held by %s", st, holder, o.ID)
			}
			if withRedis {
				if v := e.rdb.Get(ctx, holdKey(e.eventID, "Партер", new("1"), "1")).Val(); v != o.ID {
					t.Errorf("redis hold = %q, want %s", v, o.ID)
				}
				if ttl := e.rdb.PTTL(ctx, holdKey(e.eventID, "Партер", new("1"), "1")).Val(); ttl <= 0 || ttl > HoldTTL {
					t.Errorf("redis hold ttl = %v", ttl)
				}
			}

			a := e.availability(t)
			if len(a.Taken) != 2 || len(a.General) != 1 || a.General[0].Available != 17 {
				t.Errorf("availability = %+v", a)
			}

			got, err := e.svc.GetOrder(ctx, buyer, o.ID, now)
			if err != nil || got.ID != o.ID || len(got.Items) != 5 {
				t.Fatalf("GetOrder = %+v, %v", got, err)
			}
			if _, err := e.svc.GetOrder(ctx, e.buyer(t), o.ID, now); !errors.Is(err, ErrNotFound) {
				t.Errorf("other buyer GetOrder err = %v, want ErrNotFound", err)
			}

			c, err := e.svc.CancelOrder(ctx, buyer, o.ID, now)
			if err != nil || c.Status != "cancelled" {
				t.Fatalf("CancelOrder = %+v, %v", c, err)
			}
			if c2, err := e.svc.CancelOrder(ctx, buyer, o.ID, now); err != nil || c2.Status != "cancelled" {
				t.Errorf("repeated cancel = %+v, %v", c2, err)
			}
			a = e.availability(t)
			if len(a.Taken) != 0 || a.General[0].Available != 20 {
				t.Errorf("availability after cancel = %+v", a)
			}
			if withRedis {
				if n := e.rdb.Exists(ctx, holdKey(e.eventID, "Партер", new("1"), "1")).Val(); n != 0 {
					t.Error("redis hold survived cancel")
				}
			}
			// После отмены места снова продаются.
			if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), now); err != nil {
				t.Errorf("seat not bookable after cancel: %v", err)
			}
			e.checkNoDoubleBooking(t)
		})
	}
}

func (e *env) availability(t *testing.T) Availability {
	t.Helper()
	b, err := e.svc.loadAvailability(t.Context(), e.eventID, AvailabilityQuery{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var a Availability
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSeatTaken(t *testing.T) {
	for name, withRedis := range modes() {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withRedis, 2, 2, 0)
			ctx := t.Context()
			if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now()); err != nil {
				t.Fatal(err)
			}
			// Заказ целиком или ничего: свободное 1-2 не удерживается, если 1-1 занято.
			_, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 2), seat(1, 1)), time.Now())
			if c, ok := errors.AsType[*ConflictError](err); !ok || c.Code != "seat_taken" {
				t.Fatalf("err = %s, want seat_taken", fmtErr(err))
			}
			if st, _ := e.seatStatus(t, seat(1, 2)); st != "available" {
				t.Errorf("seat 1-2 = %s after failed order, want available", st)
			}
			if withRedis {
				if n := e.rdb.Exists(ctx, holdKey(e.eventID, "Партер", new("1"), "2")).Val(); n != 0 {
					t.Error("failed order left a redis hold")
				}
			}
			e.checkNoDoubleBooking(t)
		})
	}
}

// Главный инвариант (CLAUDE.md, правило 1): сотни покупателей одновременно
// берут одно место — достаётся ровно одному.
func TestConcurrentSameSeat(t *testing.T) {
	for name, withRedis := range modes() {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withRedis, 1, 1, 0)
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

// Покупатели берут случайные пересекающиеся наборы мест: ни одно место не
// оказывается в двух заказах, и взаимоблокировок нет.
func TestConcurrentOverlappingSeats(t *testing.T) {
	for name, withRedis := range modes() {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withRedis, 3, 4, 0)
			const n = 60
			buyers := make([]string, n)
			for i := range buyers {
				buyers[i] = e.buyer(t)
			}
			var wg sync.WaitGroup
			start := make(chan struct{})
			var won atomic.Int32
			for _, b := range buyers {
				picks := rand.Perm(12)[:3] //nolint:gosec // случайный выбор мест в тесте, не криптография
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
				t.Errorf("successful orders = %d, want 1..4 for 12 seats in groups of 3", won.Load())
			}
			e.checkNoDoubleBooking(t)
		})
	}
}

// Входная зона не продаётся сверх вместимости.
func TestConcurrentGeneral(t *testing.T) {
	for name, withRedis := range modes() {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withRedis, 0, 0, 30)
			const n = 70
			buyers := make([]string, n)
			for i := range buyers {
				buyers[i] = e.buyer(t)
			}
			var ok, soldOut atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for _, b := range buyers {
				wg.Go(func() {
					<-start
					_, err := e.svc.CreateOrder(t.Context(), b, e.eventID, generalReq(1), time.Now())
					if c, isConflict := errors.AsType[*ConflictError](err); isConflict && c.Code == "not_enough_seats" {
						soldOut.Add(1)
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
			if ok.Load() != 30 || soldOut.Load() != n-30 {
				t.Fatalf("successes = %d, sold out = %d; want 30 and %d", ok.Load(), soldOut.Load(), n-30)
			}
			if a := e.availability(t); a.General[0].Available != 0 {
				t.Errorf("available = %d, want 0", a.General[0].Available)
			}
			e.checkNoDoubleBooking(t)
		})
	}
}

// Новый выбор заменяет прежнюю корзину покупателя на событие.
func TestReplaceCart(t *testing.T) {
	for name, withRedis := range modes() {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withRedis, 1, 3, 0)
			ctx := t.Context()
			buyer := e.buyer(t)
			now := time.Now()

			first, err := e.svc.CreateOrder(ctx, buyer, e.eventID, seatsReq(seat(1, 1)), now)
			if err != nil {
				t.Fatal(err)
			}
			second, err := e.svc.CreateOrder(ctx, buyer, e.eventID, seatsReq(seat(1, 2)), now)
			if err != nil {
				t.Fatal(err)
			}
			if o, _ := e.svc.GetOrder(ctx, buyer, first.ID, now); o.Status != "cancelled" {
				t.Errorf("first order = %s, want cancelled", o.Status)
			}
			if withRedis {
				if n := e.rdb.Exists(ctx, holdKey(e.eventID, "Партер", new("1"), "1")).Val(); n != 0 {
					t.Error("replaced order kept its redis hold")
				}
			}
			// Место из прежней корзины свободно для других.
			if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), now); err != nil {
				t.Errorf("seat of replaced order: %v", err)
			}
			// Новая корзина может снова включить место из заменяемой.
			third, err := e.svc.CreateOrder(ctx, buyer, e.eventID, seatsReq(seat(1, 2), seat(1, 3)), now)
			if err != nil {
				t.Fatalf("re-taking own seat: %v", err)
			}
			if st, holder := e.seatStatus(t, seat(1, 2)); st != "held" || *holder != third.ID {
				t.Errorf("seat 1-2 = %s held by %v, want %s", st, holder, third.ID)
			}
			if o, _ := e.svc.GetOrder(ctx, buyer, second.ID, now); o.Status != "cancelled" {
				t.Errorf("second order = %s, want cancelled", o.Status)
			}
			e.checkNoDoubleBooking(t)
		})
	}
}

// Повтор запроса, чей заказ уже зафиксирован, хотя ответ до клиента не дошёл
// (база упала между фиксацией и ответом, ADR 028): middleware снял свой ключ,
// и запрос выполняется заново. Возвращается тот же заказ, а не замена.
func TestRetryAfterUnknownCommit(t *testing.T) {
	for name, withRedis := range modes() {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withRedis, 1, 3, 0)
			buyer := e.buyer(t)
			now := time.Now()
			ctx := idempotency.WithKey(t.Context(), "key-1")

			first, err := e.svc.CreateOrder(ctx, buyer, e.eventID, seatsReq(seat(1, 1)), now)
			if err != nil {
				t.Fatal(err)
			}
			again, err := e.svc.CreateOrder(ctx, buyer, e.eventID, seatsReq(seat(1, 1)), now)
			if err != nil {
				t.Fatal(err)
			}
			if again.ID != first.ID || again.Status != "pending" {
				t.Errorf("retry = %s %s, want the first order %s pending", again.ID, again.Status, first.ID)
			}
			var orders int
			if err := e.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM orders WHERE buyer_id = $1`, buyer).Scan(&orders); err != nil {
				t.Fatal(err)
			}
			if orders != 1 {
				t.Errorf("orders = %d, want 1", orders)
			}
			// Другой ключ — новый запрос: корзина заменяется, как обычно.
			other, err := e.svc.CreateOrder(idempotency.WithKey(t.Context(), "key-2"), buyer, e.eventID, seatsReq(seat(1, 2)), now)
			if err != nil {
				t.Fatal(err)
			}
			if other.ID == first.ID {
				t.Error("new key returned the old order")
			}
			e.checkNoDoubleBooking(t)
		})
	}
}

// Одновременные заказы одного покупателя на одно событие: остаётся ровно
// одна активная корзина.
func TestConcurrentSameBuyer(t *testing.T) {
	e := newEnv(t, false, 1, 10, 0)
	buyer := e.buyer(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 10 {
		wg.Go(func() {
			<-start
			_, err := e.svc.CreateOrder(t.Context(), buyer, e.eventID, seatsReq(seat(1, i+1)), time.Now())
			if _, isConflict := errors.AsType[*ConflictError](err); err != nil && !isConflict {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	var pending, held int
	if err := e.db.Pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM orders WHERE buyer_id = $1 AND status = 'pending'),
		(SELECT count(*) FROM event_seats WHERE event_id = $2 AND status = 'held')`, buyer, e.eventID).Scan(&pending, &held); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || held != 1 {
		t.Errorf("pending orders = %d, held seats = %d; want 1 and 1", pending, held)
	}
	e.checkNoDoubleBooking(t)
}

func TestOrderRules(t *testing.T) {
	e := newEnv(t, false, 2, 6, 20)
	now := time.Now()
	create := func(req OrderRequest, at time.Time) error {
		_, err := e.svc.CreateOrder(t.Context(), e.buyer(t), e.eventID, req, at)
		return err
	}
	code := func(err error) string {
		switch v := err.(type) { //nolint:errorlint // ошибки сервиса не оборачиваются
		case *ValidationError:
			return "invalid_" + v.Field
		case *PreconditionError:
			return v.Code
		case *ConflictError:
			return v.Code
		}
		if errors.Is(err, ErrNotFound) {
			return "not_found"
		}
		return fmtErr(err)
	}

	tooMany := seatsReq(seat(1, 1), seat(1, 2), seat(1, 3), seat(1, 4), seat(1, 5), seat(1, 6))
	tooMany.General = []GeneralRef{{Section: "Фан-зона", Quantity: 5}}
	badEmail := seatsReq(seat(1, 1))
	badEmail.Email = "not-an-email"
	cases := map[string]struct {
		req  OrderRequest
		at   time.Time
		want string
	}{
		"over limit":       {tooMany, now, "ticket_limit_exceeded"},
		"bad email":        {badEmail, now, "invalid_email"},
		"duplicate seat":   {seatsReq(seat(1, 1), seat(1, 1)), now, "invalid_seats"},
		"nothing chosen":   {seatsReq(), now, "invalid_seats"},
		"zero quantity":    {generalReq(0), now, "invalid_general"},
		"no such seat":     {seatsReq(seat(9, 9)), now, "seat_taken"},
		"no such section":  {OrderRequest{General: []GeneralRef{{Section: "VIP", Quantity: 1}}, Email: "a@b.kz"}, now, "not_enough_seats"},
		"general sold out": {generalReq(21), now, "ticket_limit_exceeded"},
		"after start":      {seatsReq(seat(2, 1)), now.Add(31 * 24 * time.Hour), "sales_closed"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := code(create(c.req, c.at)); got != c.want {
				t.Errorf("error = %s, want %s", got, c.want)
			}
		})
	}

	t.Run("sales window", func(t *testing.T) {
		e.exec(t, `UPDATE events SET sales_start_at = $2, sales_end_at = $3 WHERE id = $1`,
			e.eventID, now.Add(time.Hour), now.Add(2*time.Hour))
		t.Cleanup(func() {
			_, _ = e.db.Pool.Exec(context.Background(), `UPDATE events SET sales_start_at = NULL, sales_end_at = NULL WHERE id = $1`, e.eventID)
		})
		if got := code(create(seatsReq(seat(2, 2)), now)); got != "sales_not_started" {
			t.Errorf("before window = %s", got)
		}
		if err := create(seatsReq(seat(2, 2)), now.Add(90*time.Minute)); err != nil {
			t.Errorf("inside window: %v", err)
		}
		if got := code(create(seatsReq(seat(2, 3)), now.Add(3*time.Hour))); got != "sales_closed" {
			t.Errorf("after window = %s", got)
		}
	})

	t.Run("unpublished events", func(t *testing.T) {
		draft := e.publishEvent(t, "draft", 1, 1, 0)
		if _, err := e.svc.CreateOrder(t.Context(), e.buyer(t), draft, seatsReq(seat(1, 1)), now); !errors.Is(err, ErrNotFound) {
			t.Errorf("draft: %v", err)
		}
		if _, err := e.svc.GetAvailability(t.Context(), draft, AvailabilityQuery{}); !errors.Is(err, ErrNotFound) {
			t.Errorf("draft availability: %v", err)
		}
		if _, err := e.svc.CreateOrder(t.Context(), e.buyer(t), "not-a-uuid", seatsReq(seat(1, 1)), now); !errors.Is(err, ErrNotFound) {
			t.Errorf("bad id: %v", err)
		}
		e.exec(t, `UPDATE events SET status = 'cancelled', cancelled_at = now() WHERE id = $1`, draft)
		if got := code(create2(t, e, draft)); got != "event_cancelled" {
			t.Errorf("cancelled event = %s", got)
		}
	})
}

func create2(t *testing.T, e *env, eventID string) error {
	_, err := e.svc.CreateOrder(t.Context(), e.buyer(t), eventID, seatsReq(seat(1, 1)), time.Now())
	return err
}

// Холд истекает через HoldTTL: место снова продаётся сразу, заказ получает
// статус expired при чтении или фоновой очистке.
func TestExpiry(t *testing.T) {
	// Без Redis: в тесте время сдвигается, а TTL ключей Redis идёт по часам.
	e := newEnv(t, false, 1, 2, 5)
	past := time.Now().Add(-HoldTTL - time.Minute)
	buyer := e.buyer(t)

	req := seatsReq(seat(1, 1), seat(1, 2))
	req.General = []GeneralRef{{Section: "Фан-зона", Quantity: 2}}
	old, err := e.svc.CreateOrder(t.Context(), buyer, e.eventID, req, past)
	if err != nil {
		t.Fatal(err)
	}
	// Истёкший холд не мешает другому покупателю ещё до очистки.
	fresh, err := e.svc.CreateOrder(t.Context(), e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now())
	if err != nil {
		t.Fatalf("seat with expired hold: %v", err)
	}
	if a := e.availability(t); len(a.Taken) != 1 {
		t.Errorf("taken = %+v, want only the fresh order's seat", a.Taken)
	}

	n, err := e.svc.ExpireOrders(t.Context(), time.Now(), 100)
	if err != nil || n != 1 {
		t.Fatalf("ExpireOrders = %d, %v; want 1", n, err)
	}
	o, err := e.svc.GetOrder(t.Context(), buyer, old.ID, time.Now())
	if err != nil || o.Status != "expired" {
		t.Fatalf("old order = %+v, %v", o, err)
	}
	// Место, перехваченное свежим заказом, очистка не трогает.
	if st, holder := e.seatStatus(t, seat(1, 1)); st != "held" || *holder != fresh.ID {
		t.Errorf("seat 1-1 = %s held by %v, want fresh order", st, holder)
	}
	if st, _ := e.seatStatus(t, seat(1, 2)); st != "available" {
		t.Errorf("seat 1-2 = %s, want available", st)
	}
	if a := e.availability(t); a.General[0].Available != 5 {
		t.Errorf("general available = %d, want 5", a.General[0].Available)
	}
	if n, _ := e.svc.ExpireOrders(t.Context(), time.Now(), 100); n != 0 {
		t.Errorf("second ExpireOrders = %d, want 0", n)
	}
	if _, err := e.svc.CancelOrder(t.Context(), buyer, old.ID, time.Now()); err == nil {
		t.Error("cancelled an expired order")
	}

	t.Run("lazy on read", func(t *testing.T) {
		b := e.buyer(t)
		o, err := e.svc.CreateOrder(t.Context(), b, e.eventID, generalReq(1), past)
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.svc.GetOrder(t.Context(), b, o.ID, time.Now())
		if err != nil || got.Status != "expired" {
			t.Fatalf("GetOrder = %+v, %v; want expired", got, err)
		}
	})
	e.checkNoDoubleBooking(t)
}

// На событие со свободным входом билеты не продаются.
func TestFreeEntryEventHasNoOrders(t *testing.T) {
	e := newEnv(t, false, 0, 0, 0)
	e.exec(t, `UPDATE events SET admission = 'free_entry', seat_map_id = NULL WHERE id = $1`, e.eventID)
	_, err := e.svc.CreateOrder(t.Context(), e.buyer(t), e.eventID, generalReq(1), time.Now())
	if pe, ok := errors.AsType[*PreconditionError](err); !ok || pe.Code != "free_entry" {
		t.Errorf("err = %v, want free_entry", err)
	}
}

func TestListOrders(t *testing.T) {
	e := newEnv(t, false, 1, 3, 0)
	ctx := t.Context()
	buyer := e.buyer(t)
	first, err := e.svc.CreateOrder(ctx, buyer, e.eventID, seatsReq(seat(1, 1)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConfirmPayment(ctx, paid(first, time.Now())); err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.CreateOrder(ctx, buyer, e.eventID, seatsReq(seat(1, 2), seat(1, 3)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Заказ другого покупателя в список не попадает.
	if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now()); err == nil {
		t.Fatal("sold seat was booked by another buyer")
	}
	list, err := e.svc.ListOrders(ctx, buyer)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[0].Items != 2 || list[1].Status != "paid" || list[0].EventTitle != "Show" {
		t.Errorf("orders = %+v", list)
	}
	// Отменённая корзина в списке не показывается.
	if _, err := e.svc.CancelOrder(ctx, buyer, second.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.svc.ListOrders(ctx, buyer); len(list) != 1 {
		t.Errorf("orders after cancel = %d, want 1", len(list))
	}
}

func TestServiceFeeRounding(t *testing.T) {
	tests := []struct {
		price int64
		bps   int32
		want  int64
	}{
		{500_000, 500, 25_000}, // 5 000 ₸ → 250 ₸
		{0, 500, 0},            // бесплатный билет без сбора
		{500_000, 0, 0},        // сбор выключен
		{999, 500, 50},         // 49,95 тиына → 50, половина вверх
		{989, 500, 49},         // 49,45 → 49
		{10, 500, 1},           // 0,5 → 1
	}
	for _, tt := range tests {
		if got := ServiceFee(tt.price, tt.bps); got != tt.want {
			t.Errorf("ServiceFee(%d, %d) = %d, want %d", tt.price, tt.bps, got, tt.want)
		}
	}
}

// Сервисный сбор считается на каждый платный билет, входит в сумму заказа и
// записывается в позиции: смена ставки не меняет уже созданные заказы.
func TestServiceFee(t *testing.T) {
	e := newEnv(t, false, 2, 4, 10)
	ctx := t.Context()
	now := time.Now()
	e.svc = NewService(e.db.Pool, nil, quietLog(), WithServiceFee(500))

	req := seatsReq(seat(1, 1), seat(1, 2))
	req.General = []GeneralRef{{Section: "Фан-зона", Quantity: 1}}
	buyer := e.buyer(t)
	o, err := e.svc.CreateOrder(ctx, buyer, e.eventID, req, now)
	if err != nil {
		t.Fatal(err)
	}
	wantFee := 2*ServiceFee(partPrice, 500) + ServiceFee(fanPrice, 500)
	if o.FeeTiyn != wantFee || o.TotalTiyn != 2*partPrice+fanPrice+wantFee {
		t.Fatalf("order total = %d, fee = %d; want fee %d on top of prices", o.TotalTiyn, o.FeeTiyn, wantFee)
	}
	for _, it := range o.Items {
		if it.FeeTiyn != ServiceFee(it.PriceTiyn, 500) {
			t.Errorf("item %+v: fee = %d", it, it.FeeTiyn)
		}
	}
	if a := e.availability(t); a.ServiceFeeBps != 500 {
		t.Errorf("availability service_fee_bps = %d, want 500", a.ServiceFeeBps)
	}

	// Новая ставка действует только на новые заказы.
	e.svc = NewService(e.db.Pool, nil, quietLog(), WithServiceFee(700))
	got, err := e.svc.GetOrder(ctx, buyer, o.ID, now)
	if err != nil || got.FeeTiyn != wantFee || got.TotalTiyn != o.TotalTiyn {
		t.Errorf("order after the rate change = %+v, %v", got, err)
	}

	// Бесплатные билеты: сбора нет, заказ оформляется сразу.
	e.exec(t, `UPDATE price_categories SET price_tiyn = 0 WHERE event_id = $1`, e.eventID)
	free, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(2, 1)), now)
	if err != nil {
		t.Fatal(err)
	}
	if free.Status != "paid" || free.TotalTiyn != 0 || free.FeeTiyn != 0 {
		t.Errorf("free order = %+v, want paid with no fee", free)
	}
}

func TestAvailabilitySummaryAndSection(t *testing.T) {
	e := newEnv(t, false, 2, 3, 0)
	ctx := t.Context()
	now := time.Now()
	if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1), seat(2, 3)), now); err != nil {
		t.Fatal(err)
	}
	// Холд, который уже истёк, считается свободным местом.
	if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 2)), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	load := func(q AvailabilityQuery) Availability {
		t.Helper()
		b, err := e.svc.loadAvailability(ctx, e.eventID, q, now)
		if err != nil {
			t.Fatal(err)
		}
		var a Availability
		if err := json.Unmarshal(b, &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	parter := []SectionAvailability{{Section: "Партер", Available: 4, Total: 6}}

	for name, tc := range map[string]struct {
		q     AvailabilityQuery
		taken int
		want  []SectionAvailability
	}{
		"all":     {AvailabilityQuery{}, 2, parter},
		"summary": {AvailabilityQuery{Summary: true}, 0, parter},
		"section": {AvailabilityQuery{Section: "Партер"}, 2, parter},
		// Чужой сектор: ни мест, ни счётчиков — сводку по всему залу режим
		// сектора не считает.
		"other section": {AvailabilityQuery{Section: "Сектор 12"}, 0, nil},
	} {
		a := load(tc.q)
		if len(a.Taken) != tc.taken {
			t.Errorf("%s: taken = %v, want %d seats", name, a.Taken, tc.taken)
		}
		if !slices.Equal(a.Sections, tc.want) {
			t.Errorf("%s: sections = %+v, want %+v", name, a.Sections, tc.want)
		}
	}
}

func TestSummaryCache(t *testing.T) {
	e := newEnv(t, false, 1, 4, 0)
	ctx := t.Context()
	free := func() int32 {
		t.Helper()
		b, err := e.svc.GetAvailability(ctx, e.eventID, AvailabilityQuery{Summary: true})
		if err != nil {
			t.Fatal(err)
		}
		var a Availability
		if err := json.Unmarshal(b, &a); err != nil {
			t.Fatal(err)
		}
		return a.Sections[0].Available
	}
	e.svc.summaryTTL = time.Hour
	if got := free(); got != 4 {
		t.Fatalf("free = %d, want 4", got)
	}
	if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(1, 1)), time.Now()); err != nil {
		t.Fatal(err)
	}
	// Сводка из памяти ещё старая, места одного сектора — всегда свежие.
	if got := free(); got != 4 {
		t.Errorf("cached free = %d, want 4", got)
	}
	b, err := e.svc.GetAvailability(ctx, e.eventID, AvailabilityQuery{Section: "Партер"})
	if err != nil {
		t.Fatal(err)
	}
	var sec Availability
	if err := json.Unmarshal(b, &sec); err != nil || len(sec.Taken) != 1 {
		t.Errorf("section taken = %+v, %v", sec.Taken, err)
	}
	// Без кэша сводка свежая.
	e.svc.summaryTTL = 0
	if got := free(); got != 3 {
		t.Errorf("uncached free = %d, want 3", got)
	}
}
