package mq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Handler обрабатывает одно сообщение. Ошибка означает, что сообщение
// не обработано: оно отклоняется без возврата в очередь и, если у очереди
// включён DeadLetter, попадает в очередь <имя>.dead для разбора.
type Handler func(ctx context.Context, d *amqp.Delivery) error

// ConsumerConfig описывает, какую очередь и как потреблять.
type ConsumerConfig struct {
	Queue    string
	Prefetch int
	// Tag — имя потребителя, видимое в RabbitMQ.
	Tag string
	// DeadLetter — отклонённые сообщения уходят в очередь <Queue>.dead,
	// а не пропадают. Настройка должна совпадать у издателя и потребителя.
	DeadLetter bool
}

const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// Consume потребляет очередь до отмены ctx, переподключаясь при обрывах.
// После отмены ctx новые сообщения не берутся, текущее дорабатывается
// до конца; неподтверждённые сообщения RabbitMQ вернёт в очередь.
func Consume(ctx context.Context, c *Conn, cfg ConsumerConfig, log *slog.Logger, h Handler) {
	backoff := minBackoff
	for {
		err := consumeOnce(ctx, c, cfg, log, h, func() { backoff = minBackoff })
		if ctx.Err() != nil {
			return
		}
		log.Warn("consumer interrupted, reconnecting",
			slog.String("queue", cfg.Queue), slog.Any("error", err), slog.String("retry_in", backoff.String()))

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// consumeOnce работает в рамках одного AMQP-канала и возвращается,
// когда канал закрылся или ctx отменён.
func consumeOnce(ctx context.Context, c *Conn, cfg ConsumerConfig, log *slog.Logger, h Handler, connected func()) error {
	conn, err := c.Get(ctx)
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	if err := DeclareQueue(ch, cfg.Queue, cfg.DeadLetter); err != nil {
		return err
	}
	if err := ch.Qos(cfg.Prefetch, 0, false); err != nil {
		return fmt.Errorf("set qos: %w", err)
	}
	deliveries, err := ch.Consume(cfg.Queue, cfg.Tag, false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %q: %w", cfg.Queue, err)
	}

	connected()
	log.Info("consuming", slog.String("queue", cfg.Queue), slog.Int("prefetch", cfg.Prefetch))

	// Обработчик не должен прерываться сигналом остановки посреди работы.
	handlerCtx := context.WithoutCancel(ctx)
	for {
		select {
		case <-ctx.Done():
			stopConsuming(ch, cfg.Tag, log)
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("delivery channel closed")
			}
			// select выбирает случайно, если готовы оба случая, поэтому
			// остановку проверяем явно: после неё новые сообщения не берём.
			if ctx.Err() != nil {
				if err := d.Nack(false, true); err != nil {
					log.Warn("requeue failed", slog.Any("error", err))
				}
				stopConsuming(ch, cfg.Tag, log)
				return nil
			}
			handle(handlerCtx, cfg.Queue, &d, log, h)
		}
	}
}

// stopConsuming просит RabbitMQ больше не присылать сообщения. Уже
// присланные, но не подтверждённые вернутся в очередь при закрытии канала.
func stopConsuming(ch *amqp.Channel, tag string, log *slog.Logger) {
	if err := ch.Cancel(tag, false); err != nil && !errors.Is(err, amqp.ErrClosed) {
		log.Warn("cancel consumer", slog.Any("error", err))
	}
}

func handle(ctx context.Context, queue string, d *amqp.Delivery, log *slog.Logger, h Handler) {
	start := time.Now()
	err := h(ctx, d)
	handled.WithLabelValues(queue, result(err)).Inc()
	handleTime.WithLabelValues(queue).Observe(time.Since(start).Seconds())
	if err != nil {
		log.Error("message handling failed", slog.String("message_id", d.MessageId), slog.Any("error", err))
		if nackErr := d.Nack(false, false); nackErr != nil {
			log.Warn("nack failed", slog.Any("error", nackErr))
		}
		return
	}
	if err := d.Ack(false); err != nil {
		log.Warn("ack failed", slog.Any("error", err))
	}
}
