// Command api запускает HTTP-сервер платформы.
//
// Использование:
//
//	api              — запустить сервер
//	api healthcheck  — проверить /healthz запущенного сервера (для Docker)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/funster-a/dd/internal/booking"
	"github.com/funster-a/dd/internal/catalog"
	"github.com/funster-a/dd/internal/identity"
	"github.com/funster-a/dd/internal/payment"
	"github.com/funster-a/dd/internal/platform/auth"
	"github.com/funster-a/dd/internal/platform/config"
	"github.com/funster-a/dd/internal/platform/db"
	"github.com/funster-a/dd/internal/platform/httpx"
	"github.com/funster-a/dd/internal/platform/mq"
	"github.com/funster-a/dd/internal/platform/observability"
	"github.com/funster-a/dd/internal/platform/redis"
	"github.com/funster-a/dd/internal/platform/storage"
	"github.com/funster-a/dd/internal/ticket"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := runHealthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// JSON с самого начала: ошибка конфигурации тоже должна быть в формате логов.
	slog.SetDefault(observability.NewLogger(os.Stdout, slog.LevelInfo, "api"))
	if err := run(); err != nil {
		slog.Error("api stopped with error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := observability.NewLogger(os.Stdout, cfg.LogLevel, "api")
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	rdb := redis.NewClient(cfg.RedisAddr, log)
	rmq := mq.New(cfg.RabbitMQURL)
	closeDeps := func() {
		pool.Close()
		_ = rdb.Close()
		_ = rmq.Close()
	}

	ident := identity.NewService(pool, rdb, identity.LogSender{Log: log}, cfg.AdminEmails, log)
	store, err := storage.New(storage.Config{
		Endpoint: cfg.S3.Endpoint, PublicEndpoint: cfg.S3.PublicEndpoint,
		AccessKey: cfg.S3.AccessKey, SecretKey: cfg.S3.SecretKey,
		Bucket: cfg.S3.Bucket, Region: cfg.S3.Region,
	})
	if err != nil {
		closeDeps()
		return err
	}
	strategy, err := booking.ParseStrategy(cfg.BookingStrategy)
	if err != nil {
		closeDeps()
		return err
	}
	if strategy != booking.StrategyRedis {
		log.Warn("booking strategy for experiments, not for production", slog.String("strategy", string(strategy)))
	}
	book := booking.NewService(pool, rdb, log, booking.WithStrategy(strategy), booking.WithServiceFee(cfg.ServiceFeeBps),
		booking.WithIPTicketLimit(cfg.IPTicketLimit), booking.WithQueue(booking.DefaultQueueConfig(cfg.QueueAdmitPerSecond)))
	pay := payment.NewService(pool,
		payment.NewPSPClient(cfg.Payment.ProviderURL, cfg.Payment.APIKey, cfg.Payment.WebhookSecret),
		payment.Config{ReturnURL: cfg.PublicBaseURL + "/payment/return", CallbackURL: cfg.Payment.CallbackURL}, log)
	tickets := ticket.NewService(pool, ticket.LogMailer{Log: log},
		ticket.Config{PublicBaseURL: cfg.PublicBaseURL, SigningKey: cfg.TicketSigningKey}, log)
	cat := catalog.NewService(pool, catalog.WithObjectStore(objectStore{c: store}), catalog.WithCache(redis.NewCache(rdb)))

	r := chi.NewRouter()
	// Адрес покупателя за nginx — до всех лимитов по IP (ADR 020).
	r.Use(httpx.RealIP(cfg.TrustedProxies))
	r.Use(httpx.RequestID(log))
	r.Get("/healthz", httpx.Healthz)
	// Метрики Prometheus (ADR 017). Наружу не публикуются: nginx проксирует
	// только /v1, порт api открыт лишь внутри сети Compose.
	r.Handle("/metrics", promhttp.Handler())
	r.Get("/readyz", httpx.Readyz(map[string]httpx.Check{
		"postgres": pool.Ping,
		"redis":    func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		"rabbitmq": rmq.Ping,
		"storage":  store.Ping,
	}))
	// Страницы для браузера покупателя: возврат после оплаты и билет по ссылке.
	r.Get("/payment/return", payment.HandleReturn)
	r.Get("/t/{token}", tickets.HandlePage)
	r.Route("/v1", func(r chi.Router) {
		r.Use(ident.Middleware)
		r.Mount("/auth", ident.Routes())
		r.Mount("/public", cat.PublicRoutes())
		r.Mount("/admin", cat.AdminRoutes())
		r.Mount("/organizer", cat.OrganizerRoutes(tickets.RegisterOrganizer))
		r.Mount("/scanner", tickets.ScannerRoutes())
		book.Register(r)
		pay.Register(r)
		tickets.Register(r)
		r.With(auth.Require(auth.KindBuyer, auth.KindOrganizer, auth.KindAdmin)).Get("/me", identity.HandleMe)
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http server started", slog.String("addr", cfg.HTTPAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		closeDeps()
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	// Весь выход, включая закрытие зависимостей, укладывается в ShutdownTimeout.
	log.Info("shutting down", slog.String("timeout", cfg.ShutdownTimeout.String()))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	shutdownErr := srv.Shutdown(shutdownCtx)
	if shutdownErr == nil {
		log.Info("http server stopped")
	}

	// pgxpool.Close не принимает контекст и при зависшей базе может ждать
	// бесконечно, поэтому ждём закрытия не дольше оставшегося времени.
	closed := make(chan struct{})
	go func() {
		closeDeps()
		close(closed)
	}()
	select {
	case <-closed:
	case <-shutdownCtx.Done():
		log.Warn("dependencies did not close before shutdown timeout")
	}

	if shutdownErr != nil {
		return fmt.Errorf("shutdown http server: %w", shutdownErr)
	}
	return nil
}
