// Package observability содержит логирование и, позже, метрики.
package observability

import (
	"io"
	"log/slog"
)

// NewLogger возвращает структурный логгер, пишущий JSON в w.
func NewLogger(w io.Writer, level slog.Level, service string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(h).With(slog.String("service", service))
}
