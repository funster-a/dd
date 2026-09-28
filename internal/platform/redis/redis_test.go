package redis

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestPingHonoursContextDeadline(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	// Принимаем соединения и молчим, как зависший Redis.
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	c := NewClient(l.Addr().String(), slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := c.Ping(ctx).Err(); err == nil {
		t.Fatal("Ping() error = nil, want timeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Ping() took %v, want it bounded by the 300ms context deadline", elapsed)
	}
}
