-- Индекс под выбор мест во входной зоне (ADR 031). HoldGeneral берёт первые
-- свободные места зоны по id: с индексом (event_id, section) база сортировала
-- все свободные места зоны на каждый заказ — на стадионе это главная нагрузка
-- ведущего узла при старте продаж. С id в индексе первые N строк читаются по
-- порядку, без сортировки. Счёт свободных мест зоны идёт по тому же индексу.

-- +goose Up
CREATE INDEX event_seats_general_pick_idx
    ON event_seats (event_id, section, id) WHERE kind = 'general' AND status = 'available';
DROP INDEX event_seats_general_available_idx;

-- +goose Down
CREATE INDEX event_seats_general_available_idx
    ON event_seats (event_id, section) WHERE kind = 'general' AND status = 'available';
DROP INDEX event_seats_general_pick_idx;
