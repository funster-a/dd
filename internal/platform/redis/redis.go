// Package redis создаёт клиент Redis.
package redis

import (
	"context"
	"fmt"
	"log/slog"

	goredis "github.com/redis/go-redis/v9"
)

// NewClient создаёт клиент. Соединение открывается при первом запросе.
// Внутренние сообщения go-redis перенаправляются в log, чтобы все логи
// процесса оставались в JSON.
func NewClient(addr string, log *slog.Logger) *goredis.Client {
	goredis.SetLogger(slogAdapter{log: log})
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
