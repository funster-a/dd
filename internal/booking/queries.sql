-- Запросы модуля booking (ADR 011). Сгенерировать: make sqlc

-- name: GetBookableEvent :one
SELECT id, organizer_id, status, admission, starts_at, sales_start_at, sales_end_at, max_tickets_per_buyer, waiting_room
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

-- name: ReadSeatsForHold :many
-- Оптимистичная стратегия (ADR 017): места читаются без блокировки вместе с
-- версией строки; решение «свободно ли» принимает приложение.
SELECT s.id, s.section, s.row_label, s.seat_label, s.status, s.hold_expires_at, s.version, p.price_tiyn
FROM event_seats s
JOIN price_categories p ON p.id = s.price_category_id
WHERE s.event_id = @event_id AND s.kind = 'seat'
  AND (s.section, s.row_label, s.seat_label) IN (
      SELECT unnest(@sections::text[]), unnest(@rows::text[]), unnest(@seat_labels::text[]));

-- name: HoldSeatsIfVersion :many
-- Холд, только если версия строки не изменилась с момента чтения. Версию
-- увеличивает триггер. Вернулось меньше строк — кто-то успел раньше:
-- транзакция откатывается и попытка повторяется.
UPDATE event_seats s
SET status = 'held', hold_order_id = @order_id::uuid, hold_expires_at = @expires_at::timestamptz
FROM (SELECT unnest(@ids::uuid[]) AS id, unnest(@versions::integer[]) AS version) v
WHERE s.id = v.id AND s.version = v.version
RETURNING s.id;

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
INSERT INTO orders (id, organizer_id, event_id, buyer_id, email, total_tiyn, expires_at, client_ip, request_key)
VALUES (@id, @organizer_id, @event_id, @buyer_id, @email, @total_tiyn, @expires_at, sqlc.narg(client_ip)::inet, sqlc.narg(request_key))
RETURNING *;

-- name: InsertOrderItems :exec
INSERT INTO order_items (organizer_id, event_id, order_id, event_seat_id, price_tiyn, fee_tiyn)
SELECT @organizer_id::uuid, @event_id::uuid, @order_id::uuid, unnest(@seat_ids::uuid[]), unnest(@prices::bigint[]), unnest(@fees::bigint[]);

-- name: LockBuyerOrder :one
SELECT * FROM orders WHERE id = @id AND buyer_id = @buyer_id FOR UPDATE;

-- name: GetBuyerOrder :one
SELECT * FROM orders WHERE id = @id AND buyer_id = @buyer_id;

-- name: GetOrderByRequestKey :one
SELECT id, event_id FROM orders WHERE buyer_id = @buyer_id AND request_key = @request_key;

-- name: ListOrderItems :many
SELECT i.event_seat_id, s.kind, s.section, s.row_label, s.seat_label, i.price_tiyn, i.fee_tiyn
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
SELECT status, sales_start_at, waiting_room FROM events WHERE id = @id;

-- name: ListTakenSeats :many
-- Занятые места с рядом: проданные и под действующим холдом.
SELECT section, row_label, seat_label FROM event_seats
WHERE event_id = @event_id AND kind = 'seat'
  AND (status = 'sold' OR (status = 'held' AND hold_expires_at > @now::timestamptz))
ORDER BY section, row_label, seat_label;

-- name: ListTakenSeatsInSection :many
-- Занятые места одного сектора: схема стадиона показывает места только
-- выбранного сектора (ADR 024).
SELECT section, row_label, seat_label FROM event_seats
WHERE event_id = @event_id AND kind = 'seat' AND section = @section
  AND (status = 'sold' OR (status = 'held' AND hold_expires_at > @now::timestamptz))
ORDER BY row_label, seat_label;

-- name: CountSeatAvailability :many
-- Свободные и все места с рядом по секторам — для плана площадки.
SELECT section, count(*)::int AS total,
       count(*) FILTER (WHERE status = 'available'
                           OR (status = 'held' AND hold_expires_at <= @now::timestamptz))::int AS available
FROM event_seats
WHERE event_id = @event_id AND kind = 'seat'
GROUP BY section
ORDER BY section;

-- name: CountSectionAvailability :many
-- То же для одного сектора: открытому сектору не нужна сводка по всему
-- стадиону (ADR 026).
SELECT section, count(*)::int AS total,
       count(*) FILTER (WHERE status = 'available'
                           OR (status = 'held' AND hold_expires_at <= @now::timestamptz))::int AS available
FROM event_seats
WHERE event_id = @event_id AND kind = 'seat' AND section = @section
GROUP BY section;

-- name: CountGeneralAvailableInSection :many
-- Свободные места одной входной зоны — для открытого сектора (ADR 031). Счёт
-- идёт по частичному индексу свободных мест, а не по всем местам всех зон:
-- на старте продаж на стадионе этот запрос шёл на каждое открытие сектора.
-- Пусто, если сектор не входная зона.
SELECT z.section, (
    SELECT count(*) FROM event_seats a
    WHERE a.event_id = @event_id AND a.kind = 'general' AND a.section = @section AND a.status = 'available'
)::int AS available
FROM (SELECT section FROM event_seats
      WHERE event_id = @event_id AND kind = 'general' AND section = @section LIMIT 1) z;

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

-- name: CountBuyerTickets :one
-- Сколько билетов покупатель уже купил на событие: места оплаченных заказов,
-- кроме возвращённых билетов.
SELECT count(*)::int FROM order_items i
JOIN orders o ON o.id = i.order_id
LEFT JOIN tickets t ON t.order_item_id = i.id
WHERE o.buyer_id = @buyer_id AND o.event_id = @event_id
  AND o.status IN ('paid', 'partially_refunded')
  AND (t.id IS NULL OR t.status <> 'revoked');

-- name: CountIPTickets :one
-- Сколько билетов на событие уже взято с этого IP-адреса (ADR 020): места
-- оплаченных заказов без возвращённых и действующие корзины других
-- покупателей. Своя корзина не считается: новый заказ её заменит.
SELECT count(*)::int FROM order_items i
JOIN orders o ON o.id = i.order_id
LEFT JOIN tickets t ON t.order_item_id = i.id
WHERE o.event_id = @event_id AND o.client_ip = @client_ip::inet
  AND (
      (o.status IN ('paid', 'partially_refunded') AND (t.id IS NULL OR t.status <> 'revoked'))
      OR (o.status = 'pending' AND o.expires_at > @now AND o.buyer_id <> @buyer_id)
  );

-- name: ReleaseRefundedSeats :many
-- Места возвращённых билетов снова продаются. Возвращает места, чтобы
-- снять их ключи холдов в Redis.
UPDATE event_seats s SET status = 'available', hold_order_id = NULL, hold_expires_at = NULL
FROM tickets t
WHERE t.id = ANY(@ticket_ids::uuid[]) AND s.id = t.event_seat_id AND s.status = 'sold'
RETURNING s.id, s.kind, s.section, s.row_label, s.seat_label;

-- name: CountUnrefundedTickets :one
-- Билеты заказа, деньги за которые ещё не вернулись: возврат успешен или
-- билет бесплатный и аннулирован.
SELECT count(*)::int FROM tickets t
JOIN order_items i ON i.id = t.order_item_id
WHERE t.order_id = @order_id
  AND NOT (
      (t.status = 'revoked' AND i.price_tiyn = 0)
      OR EXISTS (
          SELECT 1 FROM refund_items ri JOIN refunds r ON r.id = ri.refund_id
          WHERE ri.ticket_id = t.id AND r.status = 'succeeded'));

-- name: SetOrderRefundStatus :exec
UPDATE orders SET status = @status, updated_at = now()
WHERE id = @id AND status IN ('paid', 'partially_refunded');

-- name: GetEventStatus :one
SELECT status FROM events WHERE id = @id;

-- name: ListBuyerOrders :many
-- Заказы покупателя для раздела «Мои билеты»: действующие и завершённые,
-- без отменённых и истёкших корзин.
SELECT o.id, o.status, o.total_tiyn, o.created_at, o.expires_at,
       e.id AS event_id, e.slug AS event_slug, e.title AS event_title, e.starts_at AS event_starts_at,
       org.slug AS organizer_slug, v.name AS venue_name, v.timezone AS venue_timezone,
       (SELECT count(*) FROM order_items i WHERE i.order_id = o.id)::int AS items
FROM orders o
JOIN events e ON e.id = o.event_id
JOIN organizers org ON org.id = o.organizer_id
JOIN venues v ON v.id = e.venue_id
WHERE o.buyer_id = @buyer_id AND o.status NOT IN ('cancelled', 'expired')
ORDER BY o.created_at DESC
LIMIT 100;
