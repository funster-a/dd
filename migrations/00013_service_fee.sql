-- +goose Up
-- Сервисный сбор с покупателя (ADR 019): сумма сбора за билет на момент
-- покупки. Сумма заказа (orders.total_tiyn) — цены билетов плюс сборы, её
-- покупатель и платит. Смена ставки не меняет старые заказы: сбор, как и
-- цена, записан в позиции.
ALTER TABLE order_items ADD COLUMN fee_tiyn bigint NOT NULL DEFAULT 0 CHECK (fee_tiyn >= 0);

-- +goose Down
ALTER TABLE order_items DROP COLUMN fee_tiyn;
