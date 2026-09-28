package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	for _, k := range []string{"HTTP_ADDR", "LOG_LEVEL", "SHUTDOWN_TIMEOUT", "DATABASE_URL", "REDIS_ADDR", "RABBITMQ_URL"} {
		t.Setenv(k, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want INFO", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 15s", cfg.ShutdownTimeout)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9999")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("SHUTDOWN_TIMEOUT", "3s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":9999" || cfg.LogLevel != slog.LevelDebug || cfg.ShutdownTimeout != 3*time.Second {
		t.Errorf("Load() = %+v, overrides not applied", cfg)
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := map[string]map[string]string{
		"bad level":        {"LOG_LEVEL": "loud"},
		"bad timeout":      {"SHUTDOWN_TIMEOUT": "soon"},
		"negative timeout": {"SHUTDOWN_TIMEOUT": "-1s"},
		"zero timeout":     {"SHUTDOWN_TIMEOUT": "0s"},
	}
	for name, env := range tests {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil, want error")
			}
		})
	}
}

func TestLoadDoesNotLeakRabbitMQPassword(t *testing.T) {
	for _, pw := range []string{"s3cr%t", "ab/cd", "ab#cd", "ab?cd"} {
		t.Setenv("RABBITMQ_URL", "amqp://dd:"+pw+"@rabbitmq:5672/")
		_, err := Load()
		if err == nil {
			// "ab#cd" и "ab?cd" дают корректный URL с другим смыслом;
			// главное — пароль не попадает в текст ошибки.
			continue
		}
		if strings.Contains(err.Error(), pw) || strings.Contains(err.Error(), "amqp://") {
			t.Errorf("error for password %q leaks it: %v", pw, err)
		}
	}
}
