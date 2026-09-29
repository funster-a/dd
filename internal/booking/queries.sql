-- Запросы модуля booking (ADR 011). Сгенерировать: make sqlc

-- name: GetBookableEvent :one
SELECT id, organizer_id, status, starts_at, sales_start_at, sales_end_at, max_tickets_per_buyer
FROM events WHERE id = @id;

-- name: LockPendingOrder :one
-- Активная корзина покупателя на событие, под блокировкой: её заменяет новый заказ.
SELECT id, expires_at FROM orders
WHERE buyer_id = @buyer_id AND event_id = @event_id AND status = 'pending'
FOR UPDATE;

-- name: HoldSeats :many
-- Холд мест с рядом по позициям. Строки блокируются в порядке id, чтобы
-- параллельные заказы с пересекающимися местами не взаимоблокировались;
-- условие на статус проверяется уже после получения блокировки, поэтому
-- место достаётся ровно одному заказу. Истёкший холд считается свободным.
WITH target AS (
    SELECT s.id FROM event_seats s
    WHERE s.event_id = @event_id AND s.kind = 'seat'
      AND (s.section, s.row_label, s.seat_label) IN (
          SELECT unnest(@sections::text[]), unnest(@rows::text[]), unnest(@seat_labels::text[]))
    ORDER BY s.id
    FOR UPDATE
)
UPDATE event_seats s
SET status = 'held', hold_order_id = @order_id::uuid, hold_expires_at = @expires_at::timestamptz
FROM target, price_categories p
WHERE s.id = target.id AND p.id = s.price_category_id
  AND (s.status = 'available' OR (s.status = 'held' AND s.hold_expires_at <= @now::timestamptz))
RETURNING s.id, s.section, s.row_label, s.seat_label, p.price_tiyn;

-- name: HoldGeneral :many
-- Холд любых свободных виртуальных мест входной зоны. SKIP LOCKED: параллельные
-- покупатели берут разные строки и не ждут друг друга.
WITH picked AS (
    SELECT g.id FROM event_seats g
    WHERE g.event_id = @event_id AND g.kind = 'general' AND g.section = @section AND g.status = 'available'
    ORDER BY g.id
    LIMIT @quantity
    FOR UPDATE SKIP LOCKED
)
UPDATE event_seats s
SET status = 'held', hold_order_id = @order_id::uuid, hold_expires_at = @expires_at::timestamptz
FROM picked, price_categories p
WHERE s.id = picked.id AND p.id = s.price_category_id
RETURNING s.id, s.section, s.row_label, s.seat_label, p.price_tiyn;

-- name: InsertOrder :one
INSERT INTO orders (id, organizer_id, event_id, buyer_id, email, total_tiyn, expires_at)
VALUES (@id, @organizer_id, @event_id, @buyer_id, @email, @total_tiyn, @expires_at)
RETURNING *;

-- name: InsertOrderItems :exec
INSERT INTO order_items (organizer_id, event_id, order_id, event_seat_id, price_tiyn)
SELECT @organizer_id::uuid, @event_id::uuid, @order_id::uuid, unnest(@seat_ids::uuid[]), unnest(@prices::bigint[]);

-- name: LockBuyerOrder :one
SELECT * FROM orders WHERE id = @id AND buyer_id = @buyer_id FOR UPDATE;

-- name: GetBuyerOrder :one
SELECT * FROM orders WHERE id = @id AND buyer_id = @buyer_id;

-- name: ListOrderItems :many
SELECT i.event_seat_id, s.kind, s.section, s.row_label, s.seat_label, i.price_tiyn
FROM order_items i JOIN event_seats s ON s.id = i.event_seat_id
WHERE i.order_id = @order_id
ORDER BY s.section, s.row_label, s.seat_label;

-- name: SetOrderStatus :one
UPDATE orders SET status = @status, updated_at = now()
WHERE id = @id AND status = 'pending'
RETURNING *;

-- name: ReleaseOrderSeats :many
-- Освобождает места, которые ещё держит заказ. Место, которое после
-- истечения холда уже взял другой заказ, не трогается.
UPDATE event_seats
SET status = 'available', hold_order_id = NULL, hold_expires_at = NULL
WHERE hold_order_id = @order_id::uuid AND status = 'held'
RETURNING id, kind, section, row_label, seat_label;

-- name: ExpireDueOrders :many
-- Пачка просроченных заказов. SKIP LOCKED: несколько воркеров не мешают друг другу.
UPDATE orders SET status = 'expired', updated_at = now()
WHERE id IN (
    SELECT o.id FROM orders o
    WHERE o.status = 'pending' AND o.expires_at <= @now::timestamptz
    ORDER BY o.expires_at
    LIMIT @batch
    FOR UPDATE SKIP LOCKED
)
RETURNING id;

-- name: ReleaseSeatsOfOrders :exec
UPDATE event_seats
SET status = 'available', hold_order_id = NULL, hold_expires_at = NULL
WHERE hold_order_id = ANY(@order_ids::uuid[]) AND status = 'held';

-- name: GetPublishedEventStatus :one
SELECT status FROM events WHERE id = @id;

-- name: ListTakenSeats :many
-- Занятые места с рядом: проданные и под действующим холдом.
SELECT section, row_label, seat_label FROM event_seats
WHERE event_id = @event_id AND kind = 'seat'
  AND (status = 'sold' OR (status = 'held' AND hold_expires_at > @now::timestamptz))
ORDER BY section, row_label, seat_label;

-- name: CountGeneralAvailable :many
SELECT section, count(*) FILTER (WHERE status = 'available')::int AS available
FROM event_seats
WHERE event_id = @event_id AND kind = 'general'
GROUP BY section
ORDER BY section;

-- name: SetOrderTotal :one
UPDATE orders SET total_tiyn = @total_tiyn, updated_at = now() WHERE id = @id RETURNING *;

-- name: LockOrder :one
SELECT * FROM orders WHERE id = @id FOR UPDATE;

-- name: SellOrderSeats :many
-- Продаёт места, которые всё ещё держит заказ (даже если срок холда уже
-- вышел, но место никто не перехватил).
UPDATE event_seats
SET status = 'sold', hold_order_id = NULL, hold_expires_at = NULL
WHERE hold_order_id = @order_id::uuid AND status = 'held'
RETURNING id;

-- name: CountOrderItems :one
SELECT count(*)::int FROM order_items WHERE order_id = @order_id;

-- name: MarkOrderPaid :one
UPDATE orders SET status = 'paid', paid_at = @paid_at::timestamptz, updated_at = now()
WHERE id = @id AND status = 'pending'
RETURNING *;
