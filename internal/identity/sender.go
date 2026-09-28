package identity

import (
	"context"
	"log/slog"

	"github.com/funster-a/dd/internal/platform/auth"
)

// Sender доставляет одноразовый код: SMS покупателю, email организатору
// и администратору.
type Sender interface {
	Send(ctx context.Context, kind auth.Kind, address, code string) error
}

// LogSender — заглушка для разработки: пишет код в лог вместо отправки.
// Реальные SMS- и email-провайдеры подключаются отдельным решением (ADR 006).
type LogSender struct {
	Log *slog.Logger
}

// Send пишет код в лог.
func (s LogSender) Send(ctx context.Context, kind auth.Kind, address, code string) error {
	s.Log.WarnContext(ctx, "one-time code (dev sender, not delivered)",
		slog.String("kind", string(kind)), slog.String("address", address), slog.String("code", code))
	return nil
}
