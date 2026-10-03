// Package redis создаёт клиент Redis.
package redis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// loggerOnce: логгер go-redis глобальный. Ставится один раз на процесс —
// иначе новый клиент перезаписывал бы его, пока соединения другого клиента
// в фоне пишут в лог (гонка, найдена тестом с недоступным Redis).
var loggerOnce sync.Once

// NewClient создаёт клиент. Соединение открывается при первом запросе.
// Внутренние сообщения go-redis перенаправляются в log, чтобы все логи
// процесса оставались в JSON.
func NewClient(addr string, log *slog.Logger) *goredis.Client {
	loggerOnce.Do(func() { goredis.SetLogger(slogAdapter{log: log}) })
	// ContextTimeoutEnabled: дедлайн контекста ограничивает и чтение/запись,
	// иначе /readyz ждал бы ReadTimeout (5 с) вместо своего бюджета.
	return goredis.NewClient(&goredis.Options{Addr: addr, ContextTimeoutEnabled: true})
}

type slogAdapter struct {
	log *slog.Logger
}

func (a slogAdapter) Printf(ctx context.Context, format string, v ...any) {
	a.log.WarnContext(ctx, fmt.Sprintf(format, v...), slog.String("component", "go-redis"))
}

// Cache — простой кэш «ключ — байты» поверх Redis.
type Cache struct {
	rdb goredis.Cmdable
}

// NewCache создаёт кэш.
func NewCache(rdb goredis.Cmdable) *Cache { return &Cache{rdb: rdb} }

// Get возвращает значение; ok = false — ключа нет.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	b, err := c.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cache get: %w", err)
	}
	return b, true, nil
}

// Set сохраняет значение на ttl.
func (c *Cache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := c.rdb.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("cache set: %w", err)
	}
	return nil
}

// Delete удаляет ключи.
func (c *Cache) Delete(ctx context.Context, keys ...string) error {
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("cache delete: %w", err)
	}
	return nil
}
