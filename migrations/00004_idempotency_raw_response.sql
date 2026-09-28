-- Повтор идемпотентного запроса должен вернуть ответ байт в байт, а jsonb
-- нормализует JSON (пробелы, порядок ключей). Храним тело как есть и
-- вместе с Content-Type.

-- +goose Up
ALTER TABLE idempotency_keys
    ALTER COLUMN response_body TYPE bytea USING convert_to(response_body::text, 'UTF8'),
    ADD COLUMN response_content_type text;

-- +goose Down
-- Таблица — кэш ответов: при откате сохранённые тела отбрасываются.
ALTER TABLE idempotency_keys
    DROP COLUMN response_content_type,
    ALTER COLUMN response_body TYPE jsonb USING NULL;
