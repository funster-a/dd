-- +goose Up
-- Очередь ожидания при старте продаж включает организатор для события
-- (бизнес-решение 2026-10-02, ADR 020). Очереди не к чему привязаться без
-- объявленного старта продаж.
ALTER TABLE events ADD COLUMN waiting_room boolean NOT NULL DEFAULT false;
ALTER TABLE events ADD CONSTRAINT events_waiting_room_needs_sales_start
    CHECK (NOT waiting_room OR sales_start_at IS NOT NULL);

-- +goose Down
ALTER TABLE events DROP CONSTRAINT events_waiting_room_needs_sales_start;
ALTER TABLE events DROP COLUMN waiting_room;
