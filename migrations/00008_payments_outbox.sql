-- Оплата, выпуск билетов и доставка событий между модулями (ADR 012).

-- +goose Up
-- Transactional outbox: событие записывается в той же транзакции, что и
-- изменение данных, и публикуется в RabbitMQ отдельно. Сообщение не
-- теряется, если процесс упал между коммитом и публикацией.
CREATE TABLE outbox (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    topic        text        NOT NULL CHECK (btrim(topic) <> ''),
    payload      jsonb       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);
CREATE INDEX outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;

-- Ссылка на страницу оплаты у провайдера: повторный запрос оплаты
-- возвращает ту же попытку, а не создаёт новую.
ALTER TABLE payments ADD COLUMN payment_url text;

-- У заказа одна действующая попытка оплаты. Иначе покупатель мог бы
-- оплатить две открытые страницы, и успешных платежей стало бы два.
CREATE UNIQUE INDEX payments_one_active_per_order_key
    ON payments (order_id) WHERE status IN ('created', 'pending');

-- Возврат за оплату после истечения холда — один на платёж: повторная
-- доставка события не вернёт деньги дважды.
CREATE UNIQUE INDEX refunds_one_late_payment_key
    ON refunds (payment_id) WHERE reason = 'late_payment';

-- +goose Down
DROP INDEX refunds_one_late_payment_key;
DROP INDEX payments_one_active_per_order_key;
ALTER TABLE payments DROP COLUMN payment_url;
DROP TABLE outbox;
