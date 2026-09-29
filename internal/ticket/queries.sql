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

-- Контроль входа (ADR 013).

-- name: CreateScannerLink :one
INSERT INTO scanner_links (organizer_id, event_id, name, token_hash)
VALUES (@organizer_id, @event_id, @name, @token_hash)
RETURNING id, event_id, name, created_at, revoked_at;

-- name: ListScannerLinks :many
SELECT id, event_id, name, created_at, revoked_at FROM scanner_links
WHERE organizer_id = @organizer_id AND event_id = @event_id
ORDER BY created_at;

-- name: RevokeScannerLink :execrows
UPDATE scanner_links SET revoked_at = now()
WHERE organizer_id = @organizer_id AND id = @id AND revoked_at IS NULL;

-- name: ScannerLinkExists :one
SELECT EXISTS (SELECT 1 FROM scanner_links WHERE organizer_id = @organizer_id AND id = @id);

-- name: GetScannerByTokenHash :one
SELECT l.id, l.organizer_id, l.event_id, l.name, l.revoked_at,
       e.title, e.starts_at, e.ends_at, v.name AS venue_name, v.timezone AS venue_timezone
FROM scanner_links l
JOIN events e ON e.id = l.event_id
JOIN venues v ON v.id = e.venue_id
WHERE l.token_hash = @token_hash;

-- name: ManifestTickets :many
-- Все билеты события для проверки без сети: id, статус и место.
SELECT t.id, t.status, s.section, s.row_label, s.seat_label
FROM tickets t JOIN event_seats s ON s.id = t.event_seat_id
WHERE t.event_id = @event_id
ORDER BY t.id;

-- name: GetScanByClient :one
SELECT sc.result, sc.ticket_id, t.used_at, s.section, s.row_label, s.seat_label
FROM ticket_scans sc
JOIN tickets t ON t.id = sc.ticket_id
JOIN event_seats s ON s.id = t.event_seat_id
WHERE sc.scanner_link_id = @scanner_link_id AND sc.client_scan_id = @client_scan_id;

-- name: LockTicketForScan :one
SELECT t.id, t.organizer_id, t.event_id, t.status, t.used_at, s.section, s.row_label, s.seat_label
FROM tickets t JOIN event_seats s ON s.id = t.event_seat_id
WHERE t.id = @id
FOR UPDATE OF t;

-- name: MarkTicketUsed :exec
UPDATE tickets SET status = 'used', used_at = @used_at::timestamptz WHERE id = @id AND status = 'issued';

-- name: MoveTicketUsedAt :exec
UPDATE tickets SET used_at = @used_at::timestamptz WHERE id = @id AND status = 'used';

-- name: DemoteAcceptedScan :exec
UPDATE ticket_scans SET result = 'duplicate' WHERE ticket_id = @ticket_id AND result = 'accepted';

-- name: InsertScan :exec
INSERT INTO ticket_scans (organizer_id, ticket_id, device_id, scanned_at, result, scanner_link_id, client_scan_id)
VALUES (@organizer_id, @ticket_id, @device_id, @scanned_at, @result, @scanner_link_id, @client_scan_id);
