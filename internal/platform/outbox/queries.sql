-- name: Insert :exec
INSERT INTO outbox (topic, payload) VALUES (@topic, @payload);

-- name: LockUnpublished :many
-- SKIP LOCKED: несколько релеев не публикуют одно сообщение одновременно.
SELECT id, topic, payload FROM outbox
WHERE published_at IS NULL
ORDER BY id
LIMIT @batch
FOR UPDATE SKIP LOCKED;

-- name: MarkPublished :exec
UPDATE outbox SET published_at = now() WHERE id = ANY(@ids::uuid[]);

-- name: DeletePublished :execrows
DELETE FROM outbox WHERE published_at < @before::timestamptz;
