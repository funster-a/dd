-- Возвраты по запросу покупателя и при отмене события (ADR 014).

-- +goose Up
-- Ключ запроса возврата: повторная доставка события не создаёт второй
-- возврат и не возвращает деньги дважды.
ALTER TABLE refunds ADD COLUMN request_id uuid UNIQUE;

-- Билет возвращается один раз.
ALTER TABLE refund_items ADD CONSTRAINT refund_items_ticket_key UNIQUE (ticket_id);

-- +goose Down
ALTER TABLE refund_items DROP CONSTRAINT refund_items_ticket_key;
ALTER TABLE refunds DROP COLUMN request_id;
