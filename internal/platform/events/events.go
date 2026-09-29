// Package events — контракты событий между модулями (ADR 012). Модули не
// импортируют друг друга, поэтому имена событий и их содержимое живут здесь.
// Событие записывает модуль-владелец данных в своей транзакции через
// outbox, доставляет RabbitMQ, обрабатывает модуль-получатель.
package events

import "time"

// Имена событий. Каждое событие читает один модуль, поэтому имя события —
// это и имя очереди (с префиксом окружения).
const (
	// PaymentSucceeded — провайдер подтвердил оплату. payment → booking.
	PaymentSucceeded = "payment.succeeded"
	// OrderPaid — заказ оплачен, места проданы. booking → ticket.
	OrderPaid = "order.paid"
	// RefundRequested — деньги нужно вернуть. booking → payment.
	RefundRequested = "refund.requested"
)

// PaymentSucceededEvent — содержимое PaymentSucceeded.
type PaymentSucceededEvent struct {
	PaymentID  string `json:"payment_id"`
	OrderID    string `json:"order_id"`
	AmountTiyn int64  `json:"amount_tiyn"`
	// PaidAt — когда платформа получила подтверждение. По нему решается,
	// успел ли покупатель оплатить до конца холда.
	PaidAt time.Time `json:"paid_at"`
}

// OrderPaidEvent — содержимое OrderPaid.
type OrderPaidEvent struct {
	OrderID string `json:"order_id"`
}

// Причины возврата (совпадают с refunds.reason).
const (
	RefundLatePayment = "late_payment"
)

// RefundRequestedEvent — содержимое RefundRequested.
type RefundRequestedEvent struct {
	PaymentID string `json:"payment_id"`
	OrderID   string `json:"order_id"`
	Reason    string `json:"reason"`
}
