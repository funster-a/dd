// Command worker обрабатывает события между модулями и фоновые задачи
// (ADR 011, ADR 012): публикует outbox в RabbitMQ, подтверждает оплату
// заказов, выпускает билеты, возвращает деньги за опоздавшую оплату и
// закрывает просроченные заказы.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/funster-a/dd/internal/booking"
	"github.com/funster-a/dd/internal/payment"
	"github.com/funster-a/dd/internal/platform/config"
	"github.com/funster-a/dd/internal/platform/db"
	"github.com/funster-a/dd/internal/platform/events"
	"github.com/funster-a/dd/internal/platform/mq"
	"github.com/funster-a/dd/internal/platform/observability"
	"github.com/funster-a/dd/internal/platform/outbox"
	"github.com/funster-a/dd/internal/ticket"
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

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	book := booking.NewService(pool, nil, log)
	pay := payment.NewService(pool,
		payment.NewPSPClient(cfg.Payment.ProviderURL, cfg.Payment.APIKey, cfg.Payment.WebhookSecret),
		payment.Config{}, log)
	tickets := ticket.NewService(pool, ticket.LogMailer{Log: log},
		ticket.Config{PublicBaseURL: cfg.PublicBaseURL, SigningKey: cfg.TicketSigningKey}, log)
	conn := mq.New(cfg.RabbitMQURL)
	pub := mq.NewPublisher(conn)

	subs := []subscription{
		subscribe(cfg.QueuePrefix, events.PaymentSucceeded, log, book.ConfirmPayment),
		subscribe(cfg.QueuePrefix, events.OrderPaid, log, tickets.Issue),
		subscribe(cfg.QueuePrefix, events.RefundRequested, log, pay.Refund),
		subscribe(cfg.QueuePrefix, events.OrderRefunded, log, book.ApplyRefund),
		subscribe(cfg.QueuePrefix, events.EventCancelled, log, tickets.HandleEventCancelled),
	}

	var tasks sync.WaitGroup
	tasks.Go(func() { mq.Consume(ctx, conn, testQueue, log, logMessage(log)) })
	for _, s := range subs {
		tasks.Go(func() { mq.Consume(ctx, conn, s.cfg, log, s.handler) })
	}
	tasks.Go(func() { outbox.NewRelay(pool, pub, cfg.QueuePrefix, log).Run(ctx) })
	tasks.Go(func() { expireOrders(ctx, book, log) })

	// Метрики воркера (ADR 022): обработка очередей, отставание outbox,
	// истёкшие заказы. Порт открыт только внутри сети Compose.
	prometheus.MustRegister(outbox.NewBacklogCollector(pool))
	metricsSrv := &http.Server{Addr: cfg.MetricsAddr, Handler: promhttp.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server", slog.Any("error", err))
		}
	}()

	<-ctx.Done()

	// Весь выход, включая закрытие соединения, укладывается в ShutdownTimeout.
	log.Info("shutting down", slog.String("timeout", cfg.ShutdownTimeout.String()))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("stop metrics server", slog.Any("error", err))
	}
	stopped := make(chan struct{})
	go func() {
		tasks.Wait()
		log.Info("consumers stopped")
		pub.Close()
		if err := conn.Close(); err != nil {
			log.Warn("close rabbitmq", slog.Any("error", err))
		}
		pool.Close()
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
