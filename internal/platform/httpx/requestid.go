// Package httpx содержит общие HTTP-компоненты: middleware и служебные эндпоинты.
package httpx

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
)

// RequestIDHeader — заголовок, в котором request id приходит от клиента
// и возвращается в ответе.
const RequestIDHeader = "X-Request-ID"

const maxRequestIDLen = 128

type ctxKey int

const (
	requestIDKey ctxKey = iota
	loggerKey
)

// RequestID берёт request id из заголовка запроса или генерирует новый,
// возвращает его в ответе и кладёт в контекст вместе с логгером,
// у которого уже есть поле request_id.
func RequestID(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if !validRequestID(id) {
				id = rand.Text()
			}
			w.Header().Set(RequestIDHeader, id)

			ctx := context.WithValue(r.Context(), requestIDKey, id)
			ctx = context.WithValue(ctx, loggerKey, base.With(slog.String("request_id", id)))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestIDFrom возвращает request id из контекста или пустую строку.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// Logger возвращает логгер запроса из контекста или slog.Default().
func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// validRequestID пропускает только короткие id из безопасных символов,
// чтобы клиент не мог подсунуть в логи произвольный текст.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, c := range []byte(id) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}
