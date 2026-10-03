package redis

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errRedis = errors.New("redis timeout")

func TestGateOpensAndProbes(t *testing.T) {
	opened := 0
	g := &Gate{Timeout: time.Second, Cooldown: time.Hour, ProbeInterval: 20 * time.Millisecond, OnOpen: func(error) { opened++ }}
	ctx := t.Context()
	fail := func(context.Context) error { return errRedis }
	ok := func(context.Context) error { return nil }

	if err := g.Do(ctx, fail); !errors.Is(err, errRedis) || !g.Open() || opened != 1 {
		t.Fatalf("first failure: err = %v, open = %v, opened = %d", err, g.Open(), opened)
	}
	// Сразу после ошибки — без вызова.
	called := false
	if err := g.Do(ctx, func(context.Context) error { called = true; return nil }); !errors.Is(err, ErrGateOpen) || called {
		t.Fatalf("call right after failure: err = %v, called = %v", err, called)
	}
	// Неудачная проба продлевает Gate и не считается новым открытием.
	time.Sleep(25 * time.Millisecond)
	if err := g.Do(ctx, fail); !errors.Is(err, errRedis) || !g.Open() || opened != 1 {
		t.Fatalf("failed probe: err = %v, open = %v, opened = %d", err, g.Open(), opened)
	}
	// Удачная проба закрывает Gate задолго до Cooldown (час).
	time.Sleep(25 * time.Millisecond)
	if err := g.Do(ctx, ok); err != nil || g.Open() {
		t.Fatalf("successful probe: err = %v, open = %v", err, g.Open())
	}
	if err := g.Do(ctx, ok); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

// Пока Gate открыт, на интервал проходит ровно одна проба, сколько бы
// запросов ни пришло одновременно.
func TestGateSingleProbe(t *testing.T) {
	g := &Gate{Timeout: time.Second, Cooldown: time.Hour, ProbeInterval: 20 * time.Millisecond}
	_ = g.Do(t.Context(), func(context.Context) error { return errRedis })
	time.Sleep(25 * time.Millisecond)

	var calls atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 100 {
		wg.Go(func() {
			<-start
			_ = g.Do(t.Context(), func(context.Context) error {
				calls.Add(1)
				time.Sleep(5 * time.Millisecond)
				return errRedis
			})
		})
	}
	close(start)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("probes = %d, want 1", n)
	}
}

func TestGateIgnoresCancelledRequests(t *testing.T) {
	g := &Gate{Timeout: time.Second, Cooldown: time.Hour}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_ = g.Do(ctx, func(c context.Context) error { return c.Err() })
	if g.Open() {
		t.Fatal("a cancelled request opened the gate")
	}
}
