// Package config читает конфигурацию процессов из переменных окружения.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"slices"
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
