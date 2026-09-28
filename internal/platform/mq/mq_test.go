package mq

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestPingInvalidURLDoesNotLeakPassword(t *testing.T) {
	c := New("amqp://dd:s3cr%t@localhost:5672/")
	err := c.Ping(t.Context())
	if err == nil {
		t.Fatal("Ping() error = nil, want error")
	}
	if strings.Contains(err.Error(), "s3cr") {
		t.Errorf("error leaks password: %v", err)
	}
}

// silentServer принимает TCP-соединения и ничего не отвечает, как зависший брокер.
func silentServer(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()
	return l.Addr().String()
}

func TestPingHonoursContextDeadline(t *testing.T) {
	c := New("amqp://dd:dd@" + silentServer(t) + "/")

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := c.Ping(ctx); err == nil {
		t.Fatal("Ping() error = nil, want timeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Ping() took %v, want it bounded by the 300ms context deadline", elapsed)
	}
}
