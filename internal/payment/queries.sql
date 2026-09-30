-- Запросы модуля payment (ADR 012). Сгенерировать: make sqlc
-- Заказы и события принадлежат другим модулям: здесь их только читают.

-- name: GetPayableOrder :one
SELECT o.id, o.organizer_id, o.status, o.total_tiyn, o.currency, o.expires_at, e.title AS event_title,
       e.status AS event_status
FROM orders o JOIN events e ON e.id = o.event_id
WHERE o.id = @id AND o.buyer_id = @buyer_id;

-- name: GetActivePayment :one
SELECT * FROM payments WHERE order_id = @order_id AND status IN ('created', 'pending');

-- name: InsertPayment :one
INSERT INTO payments (organizer_id, order_id, amount_tiyn, currency, provider)
VALUES (@organizer_id, @order_id, @amount_tiyn, @currency, @provider)
RETURNING *;

-- name: SetPaymentPending :one
UPDATE payments
SET status = 'pending', provider_payment_id = @provider_payment_id, payment_url = @payment_url, updated_at = now()
WHERE id = @id AND status IN ('created', 'pending')
RETURNING *;

-- name: SetPaymentFailed :exec
UPDATE payments SET status = 'failed', updated_at = now()
WHERE id = @id AND status IN ('created', 'pending');

-- name: InsertWebhookEvent :one
-- Повторная доставка того же уведомления не вставит строку: ErrNoRows.
INSERT INTO payment_webhook_events (provider, provider_event_id, payload)
VALUES (@provider, @provider_event_id, @payload)
ON CONFLICT DO NOTHING
RETURNING provider_event_id;

-- name: MarkWebhookProcessed :exec
UPDATE payment_webhook_events SET processed_at = now()
WHERE provider = @provider AND provider_event_id = @provider_event_id;

-- name: LockPayment :one
SELECT * FROM payments WHERE id = @id AND provider = @provider FOR UPDATE;

-- name: SetPaymentResult :one
UPDATE payments
SET status = @status, provider_payment_id = coalesce(provider_payment_id, @provider_payment_id::text), updated_at = now()
WHERE id = @id AND status IN ('created', 'pending')
RETURNING *;

-- name: GetPayment :one
SELECT * FROM payments WHERE id = @id;

-- name: GetRefund :one
SELECT * FROM refunds WHERE payment_id = @payment_id AND reason = @reason;

-- name: InsertRefund :one
INSERT INTO refunds (organizer_id, payment_id, order_id, amount_tiyn, reason)
VALUES (@organizer_id, @payment_id, @order_id, @amount_tiyn, @reason)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: SetRefundPending :exec
UPDATE refunds SET status = 'pending', updated_at = now()
WHERE id = @id AND status IN ('requested', 'pending');

-- name: SetRefundSucceeded :exec
UPDATE refunds SET status = 'succeeded', provider_refund_id = @provider_refund_id, updated_at = now()
WHERE id = @id;

-- name: SetRefundFailed :exec
UPDATE refunds SET status = 'failed', updated_at = now() WHERE id = @id;

-- name: HasSucceededPayment :one
SELECT EXISTS (SELECT 1 FROM payments WHERE order_id = @order_id AND status = 'succeeded');

-- name: GetSucceededPayment :one
SELECT * FROM payments WHERE order_id = @order_id AND status = 'succeeded';

-- name: InsertTicketRefund :one
-- Возврат билетов: повтор запроса с тем же request_id не вставит строку.
INSERT INTO refunds (organizer_id, payment_id, order_id, amount_tiyn, reason, request_id)
VALUES (@organizer_id, @payment_id, @order_id, @amount_tiyn, @reason, @request_id)
ON CONFLICT (request_id) DO NOTHING
RETURNING *;

-- name: GetRefundByRequest :one
SELECT * FROM refunds WHERE request_id = @request_id;

-- name: SumOtherRefunds :one
-- Сумма возвратов платежа, кроме этого: успешные и ещё идущие.
SELECT coalesce(sum(amount_tiyn), 0)::bigint FROM refunds
WHERE payment_id = @payment_id AND id <> @id AND status IN ('requested', 'pending', 'succeeded');

-- name: InsertRefundItems :exec
INSERT INTO refund_items (refund_id, ticket_id, organizer_id, order_id)
SELECT @refund_id::uuid, unnest(@ticket_ids::uuid[]), @organizer_id::uuid, @order_id::uuid
ON CONFLICT DO NOTHING;
