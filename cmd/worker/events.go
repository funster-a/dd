package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/funster-a/dd/internal/platform/mq"
	"github.com/funster-a/dd/internal/platform/outbox"
)

// retryDelays — паузы между попытками обработать событие внутри worker.
// Временный сбой (база перезапускается) переживается здесь; после
// последней попытки сообщение уходит в очередь <имя>.dead.
var retryDelays = []time.Duration{time.Second, 3 * time.Second}

// subscription — обработчик события между модулями (ADR 012).
type subscription struct {
	cfg     mq.ConsumerConfig
	handler mq.Handler
}

// subscribe связывает событие topic с обработчиком h, который получает
// разобранное содержимое сообщения.
func subscribe[T any](prefix, topic string, log *slog.Logger, h func(context.Context, T) error) subscription {
	queue := outbox.Queue(prefix, topic)
	return subscription{
		cfg: mq.ConsumerConfig{Queue: queue, Prefetch: 10, Tag: "dd-worker-" + topic, DeadLetter: true},
		handler: func(ctx context.Context, d *amqp.Delivery) error {
			var ev T
			if err := json.Unmarshal(d.Body, &ev); err != nil {
				return fmt.Errorf("decode %s: %w", topic, err) // повтор не поможет
			}
			var err error
			for attempt := 0; ; attempt++ {
				if err = h(ctx, ev); err == nil {
					return nil
				}
				if attempt == len(retryDelays) {
					return err
				}
				log.WarnContext(ctx, "event handling failed, retrying", slog.String("queue", queue),
					slog.String("message_id", d.MessageId), slog.Any("error", err))
				select {
				case <-ctx.Done():
					return err
				case <-time.After(retryDelays[attempt]):
				}
			}
		},
	}
}
