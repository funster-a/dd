package redis

import (
	"crypto/rand"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestCache(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR is not set")
	}
	rdb := NewClient(addr, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = rdb.Close() })
	c := NewCache(rdb)
	ctx := t.Context()
	key := "test:cache:" + rand.Text()

	if _, ok, err := c.Get(ctx, key); err != nil || ok {
		t.Fatalf("Get(missing) = ok %v, err %v", ok, err)
	}
	if err := c.Set(ctx, key, []byte("value"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if b, ok, err := c.Get(ctx, key); err != nil || !ok || string(b) != "value" {
		t.Fatalf("Get = %q, %v, %v", b, ok, err)
	}
	if ttl := rdb.TTL(ctx, key).Val(); ttl <= 0 || ttl > time.Minute {
		t.Errorf("ttl = %v", ttl)
	}
	if err := c.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Get(ctx, key); ok {
		t.Error("key survived Delete")
	}
}
