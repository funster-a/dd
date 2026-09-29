package payment

import (
	"context"
	"errors"
	"net/http"
)

// Gateway — внешний платёжный провайдер. Карта вводится на странице
// провайдера, платформа видит только результат (CLAUDE.md, правило 7).
// Реализации: PSPClient (протокол мока fakepsp); настоящий провайдер
// подключается ещё одной реализацией.
type Gateway interface {
	// Name — имя провайдера в payments.provider и в адресе вебхука.
	Name() string
	// CreatePayment создаёт платёж. Повтор с тем же PaymentID возвращает
	// тот же платёж провайдера.
	CreatePayment(ctx context.Context, req CreatePaymentRequest) (ProviderPayment, error)
	// Refund возвращает деньги. Повтор с тем же RefundID не возвращает дважды.
	Refund(ctx context.Context, req RefundRequest) (ProviderRefund, error)
	// ParseWebhook проверяет подпись уведомления и разбирает его.
	ParseWebhook(header http.Header, body []byte) (WebhookEvent, error)
}

// CreatePaymentRequest — платёж для провайдера.
type CreatePaymentRequest struct {
	PaymentID   string // наш id платежа: ключ идемпотентности у провайдера
	AmountTiyn  int64
	Currency    string
	Description string
	ReturnURL   string // куда провайдер вернёт покупателя
	CallbackURL string // куда провайдер пришлёт уведомление
}

// ProviderPayment — платёж на стороне провайдера.
type ProviderPayment struct {
	ID         string
	PaymentURL string // страница оплаты для покупателя
}

// RefundRequest — возврат для провайдера.
type RefundRequest struct {
	RefundID          string // наш id возврата: ключ идемпотентности
	ProviderPaymentID string
	AmountTiyn        int64
}

// ProviderRefund — возврат на стороне провайдера.
type ProviderRefund struct {
	ID string
}

// Типы уведомлений.
const (
	EventSucceeded = "payment.succeeded"
	EventFailed    = "payment.failed"
)

// WebhookEvent — разобранное уведомление провайдера.
type WebhookEvent struct {
	EventID           string
	Type              string
	PaymentID         string // наш id платежа
	ProviderPaymentID string
	AmountTiyn        int64
}

var (
	// ErrBadSignature — подпись уведомления не сходится.
	ErrBadSignature = errors.New("invalid webhook signature")
	// ErrRejected — провайдер отказал в операции (4xx): повтор не поможет.
	ErrRejected = errors.New("rejected by payment provider")
)
