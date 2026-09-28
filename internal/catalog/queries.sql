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
