// Package config читает конфигурацию процессов из переменных окружения.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config — общая конфигурация api и worker.
type Config struct {
	// HTTPAddr — адрес, на котором слушает HTTP-сервер api.
	HTTPAddr string
	// LogLevel — минимальный уровень логов.
	LogLevel slog.Level
	// ShutdownTimeout — сколько ждать завершения активных запросов и задач.
	ShutdownTimeout time.Duration
	// DatabaseURL — строка подключения к PostgreSQL.
	DatabaseURL string
	// RedisAddr — адрес Redis в виде host:port.
	RedisAddr string
	// RabbitMQURL — строка подключения к RabbitMQ.
	RabbitMQURL string
	// AdminEmails — администраторы платформы (ADR 006), из ADMIN_EMAILS через запятую.
	AdminEmails []string
	// S3 — хранилище файлов (ADR 009).
	S3 S3Config
	// PublicBaseURL — адрес платформы для браузера: ссылки на билеты,
	// возврат покупателя после оплаты.
	PublicBaseURL string
	// QueuePrefix — префикс имён очередей событий между модулями (ADR 012).
	QueuePrefix string
	// Payment — платёжный провайдер (ADR 012).
	Payment PaymentConfig
	// TicketSigningKey — секрет подписи ссылок на билеты.
	TicketSigningKey string
	// BookingStrategy — стратегия захвата мест (ADR 017): redis (по
	// умолчанию), pessimistic или optimistic. Значение проверяет booking.
	BookingStrategy string
	// ServiceFeeBps — сервисный сбор с покупателя в сотых долях процента
	// (ADR 019): 500 = 5 %, бизнес-решение 2026-10-02.
	ServiceFeeBps int32
	// TrustedProxies — сети обратных прокси, которым api верит в
	// X-Forwarded-For (ADR 020). По умолчанию loopback и частные сети:
	// порт api открыт только внутри сети Compose, снаружи — через nginx.
	TrustedProxies []netip.Prefix
	// QueueAdmitPerSecond — сколько покупателей в секунду очередь пропускает
	// к покупке после старта продаж (ADR 020). 0 — очереди нет.
	QueueAdmitPerSecond int
	// IPTicketLimit — сколько билетов на событие можно взять с одного
	// IP-адреса (ADR 020). 0 — без лимита.
	IPTicketLimit int
}

// PaymentConfig — параметры платёжного провайдера. Значения по умолчанию
// подходят только моку fakepsp; реальных ключей в репозитории нет
// (CLAUDE.md, правило 7).
type PaymentConfig struct {
	// ProviderURL — API провайдера для api и worker.
	ProviderURL   string
	APIKey        string
	WebhookSecret string
	// CallbackURL — адрес вебхука платформы, доступный провайдеру.
	CallbackURL string
}

// S3Config — параметры S3-совместимого хранилища.
type S3Config struct {
	// Endpoint — адрес для api; PublicEndpoint — для браузера (ссылки загрузки,
	// публичные адреса обложек). В Docker они различаются.
	Endpoint       string
	PublicEndpoint string
	AccessKey      string
	SecretKey      string
	Bucket         string
	Region         string
}

// Load читает конфигурацию из окружения. Незаданные переменные получают
// значения для локальной разработки, совпадающие с docker-compose.yml.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:    getenv("HTTP_ADDR", ":8080"),
		DatabaseURL: getenv("DATABASE_URL", "postgres://dd:dd@localhost:5432/dd?sslmode=disable"),
		RedisAddr:   getenv("REDIS_ADDR", "localhost:6379"),
		RabbitMQURL: getenv("RABBITMQ_URL", "amqp://dd:dd@localhost:5672/"),
		S3: S3Config{
			Endpoint:  getenv("S3_ENDPOINT", "http://localhost:8333"),
			AccessKey: getenv("S3_ACCESS_KEY", "dd"),
			SecretKey: getenv("S3_SECRET_KEY", "dd-secret-key"),
			Bucket:    getenv("S3_BUCKET", "dd-media"),
			Region:    getenv("S3_REGION", "us-east-1"),
		},
	}
	cfg.S3.PublicEndpoint = getenv("S3_PUBLIC_ENDPOINT", cfg.S3.Endpoint)
	cfg.PublicBaseURL = strings.TrimRight(getenv("PUBLIC_BASE_URL", "http://localhost:8080"), "/")
	cfg.QueuePrefix = getenv("QUEUE_PREFIX", "dd.")
	cfg.Payment = PaymentConfig{
		ProviderURL:   getenv("PSP_URL", "http://localhost:8090"),
		APIKey:        getenv("PSP_API_KEY", "dev-psp-api-key"),
		WebhookSecret: getenv("PSP_WEBHOOK_SECRET", "dev-psp-webhook-secret"),
		CallbackURL:   getenv("PAYMENT_CALLBACK_URL", cfg.PublicBaseURL+"/v1/payments/webhooks/fakepsp"),
	}
	cfg.TicketSigningKey = getenv("TICKET_SIGNING_KEY", "dev-ticket-signing-key-change-me")
	cfg.BookingStrategy = getenv("BOOKING_STRATEGY", "redis")

	var errs []error

	if err := cfg.LogLevel.UnmarshalText([]byte(getenv("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	timeout, err := time.ParseDuration(getenv("SHUTDOWN_TIMEOUT", "15s"))
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("SHUTDOWN_TIMEOUT: %w", err))
	case timeout <= 0:
		errs = append(errs, errors.New("SHUTDOWN_TIMEOUT: must be positive"))
	}
	cfg.ShutdownTimeout = timeout

	if _, _, err := net.SplitHostPort(cfg.HTTPAddr); err != nil {
		errs = append(errs, fmt.Errorf("HTTP_ADDR: %w", err))
	}
	if _, _, err := net.SplitHostPort(cfg.RedisAddr); err != nil {
		errs = append(errs, fmt.Errorf("REDIS_ADDR: %w", err))
	}
	if err := validateURL(cfg.RabbitMQURL, "amqp", "amqps"); err != nil {
		errs = append(errs, fmt.Errorf("RABBITMQ_URL: %w", err))
	}
	if err := validateURL(cfg.S3.Endpoint, "http", "https"); err != nil {
		errs = append(errs, fmt.Errorf("S3_ENDPOINT: %w", err))
	}
	if err := validateURL(cfg.S3.PublicEndpoint, "http", "https"); err != nil {
		errs = append(errs, fmt.Errorf("S3_PUBLIC_ENDPOINT: %w", err))
	}
	for name, v := range map[string]string{
		"PUBLIC_BASE_URL": cfg.PublicBaseURL, "PSP_URL": cfg.Payment.ProviderURL, "PAYMENT_CALLBACK_URL": cfg.Payment.CallbackURL,
	} {
		if err := validateURL(v, "http", "https"); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	if len(cfg.TicketSigningKey) < 16 {
		errs = append(errs, errors.New("TICKET_SIGNING_KEY: must be at least 16 characters"))
	}
	for e := range strings.SplitSeq(os.Getenv("ADMIN_EMAILS"), ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if !strings.Contains(e, "@") {
			errs = append(errs, fmt.Errorf("ADMIN_EMAILS: %q is not an email", e))
			continue
		}
		cfg.AdminEmails = append(cfg.AdminEmails, e)
	}
	// DATABASE_URL разбирает pgx при создании пула: он принимает и URL,
	// и формат key=value, а пароль в ошибках скрывает.

	for v := range strings.SplitSeq(getenv("TRUSTED_PROXIES", "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"), ",") {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		p, err := netip.ParsePrefix(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("TRUSTED_PROXIES: %w", err))
			continue
		}
		cfg.TrustedProxies = append(cfg.TrustedProxies, p.Masked())
	}
	cfg.QueueAdmitPerSecond = getInt(&errs, "QUEUE_ADMIT_PER_SECOND", 50, 0, 100000)
	cfg.IPTicketLimit = getInt(&errs, "IP_TICKET_LIMIT", 40, 0, 100000)

	fee, err := strconv.ParseInt(getenv("SERVICE_FEE_BPS", "500"), 10, 32)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("SERVICE_FEE_BPS: %w", err))
	case fee < 0 || fee > 3000:
		errs = append(errs, errors.New("SERVICE_FEE_BPS: must be between 0 and 3000 (0-30 %)"))
	}
	cfg.ServiceFeeBps = int32(fee) //nolint:gosec // проверено выше

	return cfg, errors.Join(errs...)
}

// validateURL проверяет схему и хост. Текст ошибки никогда не содержит
// сам URL: в нём пароль, а ошибки url.Parse цитируют исходную строку.
func validateURL(raw string, schemes ...string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid URL; special characters in user or password must be percent-encoded")
	}
	if !slices.Contains(schemes, u.Scheme) {
		return fmt.Errorf("scheme must be one of %v", schemes)
	}
	if u.Host == "" {
		return errors.New("host is empty")
	}
	return nil
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

// getInt читает целое из окружения в границах [lo, hi].
func getInt(errs *[]error, name string, def, lo, hi int) int {
	v, err := strconv.Atoi(getenv(name, strconv.Itoa(def)))
	switch {
	case err != nil:
		*errs = append(*errs, fmt.Errorf("%s: %w", name, err))
	case v < lo || v > hi:
		*errs = append(*errs, fmt.Errorf("%s: must be between %d and %d", name, lo, hi))
	}
	return v
}
