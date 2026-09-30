-- name: CreateOrganizer :one
INSERT INTO organizers (name, slug) VALUES (@name, @slug)
RETURNING id, name, slug, created_at;

-- name: CreateOwner :one
INSERT INTO organizer_members (organizer_id, email, role) VALUES (@organizer_id, @email, 'owner')
RETURNING id;

-- name: ListOrganizers :many
-- Для админки платформы: все организаторы с email владельца, новые сверху.
SELECT o.id, o.name, o.slug, o.created_at, m.email AS owner_email
FROM organizers o
JOIN organizer_members m ON m.organizer_id = o.id AND m.role = 'owner'
ORDER BY o.id DESC
LIMIT @page_size;

-- name: CreateVenue :one
INSERT INTO venues (organizer_id, name, address, timezone, latitude, longitude)
VALUES (@organizer_id, @name, @address, @timezone, @latitude, @longitude)
RETURNING *;

-- name: UpdateVenue :one
UPDATE venues
SET name = @name, address = @address, timezone = @timezone,
    latitude = @latitude, longitude = @longitude, updated_at = now()
WHERE organizer_id = @organizer_id AND id = @id
RETURNING *;

-- name: GetVenue :one
SELECT * FROM venues
WHERE organizer_id = @organizer_id AND id = @id;

-- name: ListVenues :many
SELECT * FROM venues
WHERE organizer_id = @organizer_id
ORDER BY name, id;

-- name: CreateSeatMap :one
INSERT INTO seat_maps (organizer_id, venue_id, name, layout)
VALUES (@organizer_id, @venue_id, @name, @layout)
RETURNING *;

-- name: GetSeatMap :one
SELECT * FROM seat_maps
WHERE organizer_id = @organizer_id AND id = @id;

-- name: ListSeatMaps :many
SELECT * FROM seat_maps
WHERE organizer_id = @organizer_id AND venue_id = @venue_id
ORDER BY name, id;

-- name: CreateEvent :one
INSERT INTO events (organizer_id, venue_id, seat_map_id, admission, slug, title, description, age_rating,
                    starts_at, ends_at, sales_start_at, sales_end_at,
                    max_tickets_per_buyer, refund_deadline_hours)
VALUES (@organizer_id, @venue_id, @seat_map_id, @admission, @slug, @title, @description, @age_rating,
        @starts_at, @ends_at, @sales_start_at, @sales_end_at,
        @max_tickets_per_buyer, @refund_deadline_hours)
RETURNING *;

-- name: UpdateDraftEvent :one
-- Черновик меняется целиком; опубликованное событие этим запросом не меняется.
UPDATE events
SET venue_id = @venue_id, seat_map_id = @seat_map_id, admission = @admission, slug = @slug, title = @title,
    description = @description, age_rating = @age_rating,
    starts_at = @starts_at, ends_at = @ends_at,
    sales_start_at = @sales_start_at, sales_end_at = @sales_end_at,
    max_tickets_per_buyer = @max_tickets_per_buyer, refund_deadline_hours = @refund_deadline_hours,
    updated_at = now()
WHERE organizer_id = @organizer_id AND id = @id AND status = 'draft'
RETURNING *;

-- name: GetEvent :one
SELECT * FROM events WHERE organizer_id = @organizer_id AND id = @id;

-- name: GetEventForUpdate :one
SELECT * FROM events WHERE organizer_id = @organizer_id AND id = @id FOR UPDATE;

-- name: ListEvents :many
SELECT * FROM events WHERE organizer_id = @organizer_id ORDER BY starts_at DESC, id;

-- name: SetEventMedia :one
UPDATE events
SET cover_image_key = @cover_image_key, cover_video_key = @cover_video_key, updated_at = now()
WHERE organizer_id = @organizer_id AND id = @id AND status <> 'cancelled'
RETURNING *;

-- name: MarkEventPublished :exec
UPDATE events SET status = 'published', published_at = now(), updated_at = now()
WHERE organizer_id = @organizer_id AND id = @id AND status = 'draft';

-- name: DeleteEventPrices :exec
-- Цены черновика заменяются целиком: сначала привязки, потом категории.
DELETE FROM price_categories WHERE organizer_id = @organizer_id AND event_id = @event_id;

-- name: CreatePriceCategory :one
INSERT INTO price_categories (organizer_id, event_id, name, price_tiyn)
VALUES (@organizer_id, @event_id, @name, @price_tiyn)
RETURNING *;

-- name: CreateSectionPrice :exec
INSERT INTO event_section_prices (organizer_id, event_id, section, price_category_id)
VALUES (@organizer_id, @event_id, @section, @price_category_id);

-- name: ListPriceCategories :many
SELECT * FROM price_categories WHERE organizer_id = @organizer_id AND event_id = @event_id ORDER BY price_tiyn DESC, name;

-- name: ListSectionPrices :many
SELECT section, price_category_id FROM event_section_prices
WHERE organizer_id = @organizer_id AND event_id = @event_id ORDER BY section;

-- name: InsertEventSeats :copyfrom
INSERT INTO event_seats (organizer_id, event_id, price_category_id, kind, section, row_label, seat_label)
VALUES (@organizer_id, @event_id, @price_category_id, @kind, @section, @row_label, @seat_label);

-- name: CountEventSeats :one
SELECT count(*) FROM event_seats WHERE event_id = @event_id;

-- name: GetPublishedEvent :one
-- Публичная страница: опубликованные события и отменённые после публикации
-- (покупатель по старой ссылке видит, что событие отменено).
SELECT e.*, o.slug AS organizer_slug, o.name AS organizer_name,
       v.name AS venue_name, v.address AS venue_address, v.timezone AS venue_timezone,
       v.latitude AS venue_latitude, v.longitude AS venue_longitude,
       m.layout AS seat_map_layout
FROM events e
JOIN organizers o ON o.id = e.organizer_id
JOIN venues v ON v.id = e.venue_id
LEFT JOIN seat_maps m ON m.id = e.seat_map_id
WHERE o.slug = @organizer_slug AND e.slug = @event_slug AND e.status IN ('published', 'cancelled');

-- name: GetOrganizerSlug :one
SELECT slug FROM organizers WHERE id = @id;

-- name: CancelEvent :one
-- Отмена события: опубликованное или черновик. Отменённое не меняется.
UPDATE events SET status = 'cancelled', cancelled_at = now(), updated_at = now()
WHERE organizer_id = @organizer_id AND id = @id AND status <> 'cancelled'
RETURNING *;
