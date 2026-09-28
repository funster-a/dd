-- name: UpsertBuyerByPhone :one
-- Покупатель создаётся при первом успешном входе.
INSERT INTO buyers (phone) VALUES (@phone)
ON CONFLICT (phone) DO UPDATE SET phone = EXCLUDED.phone
RETURNING id;

-- name: GetOwnerByEmail :one
SELECT id, organizer_id FROM organizer_members
WHERE email = @email AND role = 'owner';
