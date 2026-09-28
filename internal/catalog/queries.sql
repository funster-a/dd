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
