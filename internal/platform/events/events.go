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
	// RefundRequested — деньги нужно вернуть. booking, ticket → payment.
	RefundRequested = "refund.requested"
	// OrderRefunded — деньги за билеты вернулись. payment, ticket → booking.
	OrderRefunded = "order.refunded"
	// EventCancelled — организатор отменил событие. catalog → ticket.
	EventCancelled = "event.cancelled"
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
	RefundLatePayment    = "late_payment"
	RefundBuyerRequest   = "buyer_request"
	RefundEventCancelled = "event_cancelled"
)

// RefundRequestedEvent — содержимое RefundRequested. Без TicketIDs это
// возврат всего платежа за неоформленный заказ (опоздавшая оплата); с
// ними — возврат стоимости этих билетов оплаченного заказа.
type RefundRequestedEvent struct {
	// RequestID — ключ идемпотентности возврата билетов.
	RequestID string `json:"request_id,omitempty"`
	// PaymentID — платёж; для возврата билетов платёж находится по заказу.
	PaymentID  string   `json:"payment_id,omitempty"`
	OrderID    string   `json:"order_id"`
	Reason     string   `json:"reason"`
	TicketIDs  []string `json:"ticket_ids,omitempty"`
	AmountTiyn int64    `json:"amount_tiyn,omitempty"`
}

// OrderRefundedEvent — содержимое OrderRefunded.
type OrderRefundedEvent struct {
	OrderID   string   `json:"order_id"`
	TicketIDs []string `json:"ticket_ids"`
}

// EventCancelledEvent — содержимое EventCancelled.
type EventCancelledEvent struct {
	EventID string `json:"event_id"`
}
