-- +goose Up
-- IP-адрес, с которого оформлен заказ (ADR 020): лимит билетов на событие с
-- одного адреса. NULL — заказ создан не через HTTP (инструменты, тесты).
ALTER TABLE orders ADD COLUMN client_ip inet;

CREATE INDEX orders_event_client_ip_idx ON orders (event_id, client_ip)
    WHERE client_ip IS NOT NULL AND status IN ('pending', 'paid', 'partially_refunded');

-- +goose Down
DROP INDEX orders_event_client_ip_idx;
ALTER TABLE orders DROP COLUMN client_ip;
