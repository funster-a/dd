// Package outbox — transactional outbox (ADR 012). Модуль записывает
// событие в таблицу outbox той же транзакцией, что и изменение своих
// данных; Relay публикует записи в RabbitMQ и помечает опубликованными.
//
// Доставка «хотя бы один раз»: если процесс упал между публикацией и
// пометкой, сообщение уйдёт повторно. Поэтому получатели идемпотентны.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/funster-a/dd/internal/platform/outbox/outboxdb"
)

// Add записывает событие в outbox в рамках транзакции tx.
func Add(ctx context.Context, tx outboxdb.DBTX, topic string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", topic, err)
	}
	if err := outboxdb.New(tx).Insert(ctx, outboxdb.InsertParams{Topic: topic, Payload: b}); err != nil {
		return fmt.Errorf("add %s event: %w", topic, err)
	}
	return nil
}

// Queue — имя очереди события в окружении с префиксом prefix.
func Queue(prefix, topic string) string { return prefix + topic }

// Publisher публикует одно сообщение. Реализация — mq.Publisher.
type Publisher interface {
	Publish(ctx context.Context, queue string, deadLetter bool, messageID string, body []byte) error
}

// Relay переносит события из outbox в RabbitMQ.
type Relay struct {
	pool   *pgxpool.Pool
	pub    Publisher
	prefix string
	log    *slog.Logger

	// Interval — пауза, когда публиковать нечего.
	Interval time.Duration
	// Batch — сколько событий публикуется за одну транзакцию.
	Batch int32
	// Retention — сколько хранить опубликованные события для разбора.
	Retention time.Duration
}

// NewRelay создаёт релей. prefix добавляется к имени события в имени очереди.
func NewRelay(pool *pgxpool.Pool, pub Publisher, prefix string, log *slog.Logger) *Relay {
	return &Relay{pool: pool, pub: pub, prefix: prefix, log: log,
		Interval: 200 * time.Millisecond, Batch: 100, Retention: 7 * 24 * time.Hour}
}

// Run публикует события, пока ctx не отменён.
func (r *Relay) Run(ctx context.Context) {
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	for {
		n, err := r.RelayOnce(ctx)
		if err != nil && ctx.Err() == nil {
			r.log.Error("outbox relay", slog.Any("error", err))
		}
		wait := r.Interval
		if n == int(r.Batch) && err == nil {
			wait = 0 // есть ещё — не ждём
		}
		select {
		case <-ctx.Done():
			return
		case <-cleanup.C:
			if n, err := outboxdb.New(r.pool).DeletePublished(ctx, time.Now().Add(-r.Retention)); err != nil {
				r.log.Warn("outbox cleanup", slog.Any("error", err))
			} else if n > 0 {
				r.log.Info("outbox cleaned", slog.Int64("deleted", n))
			}
		case <-time.After(wait):
		}
	}
}

// RelayOnce публикует одну пачку и возвращает число опубликованных событий.
// Если публикация оборвалась на середине, успешно опубликованные всё равно
// помечаются.
func (r *Relay) RelayOnce(ctx context.Context) (int, error) {
	var published []string
	var pubErr error
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := outboxdb.New(tx)
		rows, err := q.LockUnpublished(ctx, r.Batch)
		if err != nil {
			return fmt.Errorf("lock outbox: %w", err)
		}
		for _, row := range rows {
			if err := r.pub.Publish(ctx, Queue(r.prefix, row.Topic), true, row.ID, row.Payload); err != nil {
				pubErr = err
				break
			}
			published = append(published, row.ID)
		}
		if len(published) == 0 {
			return nil
		}
		return q.MarkPublished(ctx, published)
	})
	if err != nil {
		return 0, err
	}
	return len(published), pubErr
}
