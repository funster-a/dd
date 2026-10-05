package idempotency

import (
	"context"
	"crypto/rand"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/platform/idempotency/idempotencydb"
)

// Владелец ключа (ADR 027). Экземпляр api, занявший ключ, записывается в
// него и раз в heartbeatEvery отмечается в api_instances. Повтор клиента,
// пришедший на другой экземпляр, снимает незавершённый ключ, если владелец
// не отмечался дольше deadAfter, — не дожидаясь staleAfter.
const (
	heartbeatEvery = time.Second
	deadAfter      = 5 * time.Second
)

// owner — id этого экземпляра; пусто, пока Heartbeat не запущен (тесты,
// воркер) — тогда брошенный ключ снимается только по staleAfter.
var owner atomic.Pointer[string]

func currentOwner() *string { return owner.Load() }

// Heartbeat отмечает экземпляр живым до вызова stop; stop удаляет отметку и
// ждёт этого. Вызывать stop — после остановки HTTP-сервера: запросов в
// работе уже нет, и ключи экземпляра сразу свободны. Своё соединение, а не пул: под нагрузкой пул занят
// запросами, и отметка не должна ждать в его очереди — иначе живой
// экземпляр сочли бы упавшим.
func Heartbeat(ctx context.Context, cfg *pgx.ConnConfig, log *slog.Logger) (stop func(), err error) {
	id := "api-" + rand.Text()[:12]
	conn, err := pgx.ConnectConfig(ctx, cfg.Copy())
	if err != nil {
		return nil, err
	}
	q := idempotencydb.New(conn)
	if err := q.Heartbeat(ctx, id); err != nil {
		_ = conn.Close(context.Background())
		return nil, err
	}
	if err := q.PruneInstances(ctx); err != nil {
		log.Warn("prune api instances", slog.Any("error", err))
	}
	owner.Store(&id)
	log.Info("idempotency owner registered", slog.String("instance", id))

	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				bg, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				if err := q.Leave(bg, id); err != nil {
					log.Warn("leave api instances", slog.Any("error", err))
				}
				_ = conn.Close(bg)
				cancel()
				return
			case <-t.C:
				hctx, cancel := context.WithTimeout(ctx, heartbeatEvery)
				if err := q.Heartbeat(hctx, id); err != nil && ctx.Err() == nil {
					log.Warn("idempotency heartbeat", slog.Any("error", err))
					// Соединение могло порваться: пробуем открыть новое.
					if c, err := pgx.ConnectConfig(hctx, cfg.Copy()); err == nil {
						_ = conn.Close(context.Background())
						conn, q = c, idempotencydb.New(c)
					}
				}
				cancel()
			}
		}
	}()
	return func() {
		cancel()
		<-done
		owner.Store(nil)
	}, nil
}
