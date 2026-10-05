-- Ключ запроса, создавшего заказ (ADR 028). Пишется в той же транзакции,
-- что и заказ. Если база упала после фиксации, но до ответа приложению,
-- исход неизвестен: middleware идемпотентности снимает свой ключ, и повтор
-- клиента выполняет запрос заново. Тогда повтор находит заказ по этому ключу
-- и возвращает его, а не создаёт второй.

-- +goose Up
ALTER TABLE orders ADD COLUMN request_key text;
CREATE UNIQUE INDEX orders_buyer_request_key ON orders (buyer_id, request_key) WHERE request_key IS NOT NULL;

-- +goose Down
DROP INDEX orders_buyer_request_key;
ALTER TABLE orders DROP COLUMN request_key;
