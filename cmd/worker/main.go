// Command worker запускает потребителя очереди RabbitMQ.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/funster-a/dd/internal/platform/config"
	"github.com/funster-a/dd/internal/platform/mq"
	"github.com/funster-a/dd/internal/platform/observability"
)

// testQueue — очередь каркаса для проверки связки с RabbitMQ.
// Будет заменена реальными очередями вместе с бизнес-логикой.
var testQueue = mq.ConsumerConfig{Queue: "dd.test", Prefetch: 10, Tag: "dd-worker"}

func main() {
	// JSON с самого начала: ошибка конфигурации тоже должна быть в формате логов.
	slog.SetDefault(observability.NewLogger(os.Stdout, slog.LevelInfo, "worker"))
	if err := run(); err != nil {
		slog.Error("worker stopped with error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := observability.NewLogger(os.Stdout, cfg.LogLevel, "worker")
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conn := mq.New(cfg.RabbitMQURL)

	done := make(chan struct{})
	go func() {
		mq.Consume(ctx, conn, testQueue, log, logMessage(log))
		close(done)
	}()

	<-ctx.Done()

	// Весь выход, включая закрытие соединения, укладывается в ShutdownTimeout.
	log.Info("shutting down", slog.String("timeout", cfg.ShutdownTimeout.String()))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	stopped := make(chan struct{})
	go func() {
		<-done
		log.Info("consumer stopped")
		if err := conn.Close(); err != nil {
			log.Warn("close rabbitmq", slog.Any("error", err))
		}
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		log.Warn("worker did not stop before shutdown timeout")
	}
	return nil
}

// logMessage — обработчик тестовой очереди: только пишет сообщение в лог.
func logMessage(log *slog.Logger) mq.Handler {
	return func(ctx context.Context, d *amqp.Delivery) error {
		log.InfoContext(ctx, "message received",
			slog.String("queue", testQueue.Queue),
			slog.String("message_id", d.MessageId),
			slog.Int("size", len(d.Body)),
			slog.String("body", string(d.Body)))
		return nil
	}
}
