-- Владелец незавершённого ключа идемпотентности (ADR 027). Экземпляр api,
-- занявший ключ, записывается в owner и раз в секунду отмечается в
-- api_instances. Если экземпляр упал посреди запроса, его ключ не ждёт
-- минуту: повтор клиента видит, что владелец давно не отмечался, и
-- выполняет запрос заново.

-- +goose Up
CREATE TABLE api_instances (
    id         text        PRIMARY KEY,
    started_at timestamptz NOT NULL DEFAULT now(),
    seen_at    timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE idempotency_keys ADD COLUMN owner text;

-- +goose Down
ALTER TABLE idempotency_keys DROP COLUMN owner;
DROP TABLE api_instances;
