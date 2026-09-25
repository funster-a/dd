// Package config читает конфигурацию процессов из переменных окружения.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
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
}

// Load читает конфигурацию из окружения. Незаданные переменные получают
// значения для локальной разработки, совпадающие с docker-compose.yml.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:    getenv("HTTP_ADDR", ":8080"),
		DatabaseURL: getenv("DATABASE_URL", "postgres://dd:dd@localhost:5432/dd?sslmode=disable"),
		RedisAddr:   getenv("REDIS_ADDR", "localhost:6379"),
		RabbitMQURL: getenv("RABBITMQ_URL", "amqp://dd:dd@localhost:5672/"),
	}

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

	return cfg, errors.Join(errs...)
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
