-- Индексы бронирования (ADR 011).

-- +goose Up
-- У покупателя одна активная корзина на событие: новый выбор заменяет
-- старый (spec.md). Индекс держит правило и при параллельных запросах.
CREATE UNIQUE INDEX orders_one_pending_per_buyer_event_key
    ON orders (buyer_id, event_id) WHERE status = 'pending';

-- Выбор свободных виртуальных мест входной зоны.
CREATE INDEX event_seats_general_available_idx
    ON event_seats (event_id, section) WHERE kind = 'general' AND status = 'available';

-- Освобождение мест истёкших заказов.
CREATE INDEX event_seats_hold_order_idx
    ON event_seats (hold_order_id) WHERE status = 'held';

-- +goose Down
DROP INDEX event_seats_hold_order_idx;
DROP INDEX event_seats_general_available_idx;
DROP INDEX orders_one_pending_per_buyer_event_key;
