-- Запросы модуля ticket (ADR 012). Сгенерировать: make sqlc
-- Заказы, места и события принадлежат другим модулям: здесь их только читают.

-- name: IssueTickets :many
-- Билет на каждую позицию оплаченного заказа. Повтор ничего не добавит
-- (order_item_id уникален). Второй действующий билет на место не вставится
-- из-за tickets_one_active_per_seat_key — главный инвариант на уровне схемы.
INSERT INTO tickets (organizer_id, event_id, order_id, order_item_id, event_seat_id)
SELECT i.organizer_id, i.event_id, i.order_id, i.id, i.event_seat_id
FROM order_items i JOIN orders o ON o.id = i.order_id
WHERE i.order_id = @order_id AND o.status = 'paid'
ON CONFLICT (order_item_id) DO NOTHING
RETURNING id;

-- name: GetOrderDelivery :one
SELECT o.buyer_id, o.email, o.status, e.title, e.starts_at
FROM orders o JOIN events e ON e.id = o.event_id
WHERE o.id = @id;

-- name: ListOrderTickets :many
SELECT t.id, t.status, s.kind, s.section, s.row_label, s.seat_label
FROM tickets t JOIN event_seats s ON s.id = t.event_seat_id
WHERE t.order_id = @order_id
ORDER BY s.section, s.row_label, s.seat_label;

-- name: GetTicket :one
SELECT t.id, t.status, s.kind, s.section, s.row_label, s.seat_label,
       e.title, e.starts_at, e.ends_at, e.age_rating,
       v.name AS venue_name, v.address AS venue_address, v.timezone AS venue_timezone
FROM tickets t
JOIN event_seats s ON s.id = t.event_seat_id
JOIN events e ON e.id = t.event_id
JOIN venues v ON v.id = e.venue_id
WHERE t.id = @id;
