-- name: Claim :execrows
-- Занимает ключ. 0 строк — ключ уже есть (выполнен, выполняется или брошен).
INSERT INTO idempotency_keys (scope, key, request_hash, owner)
VALUES (@scope, @key, @request_hash, sqlc.narg('owner'))
ON CONFLICT (scope, key) DO NOTHING;

-- name: Get :one
SELECT request_hash, response_status, response_body, response_content_type, created_at, completed_at
FROM idempotency_keys
WHERE scope = @scope AND key = @key;

-- name: Complete :exec
UPDATE idempotency_keys
SET response_status = @response_status, response_body = @response_body,
    response_content_type = @response_content_type, completed_at = now()
WHERE scope = @scope AND key = @key;

-- name: Release :exec
-- Снимает незавершённый ключ, чтобы клиент мог повторить запрос.
DELETE FROM idempotency_keys
WHERE scope = @scope AND key = @key AND completed_at IS NULL;

-- name: ReleaseStale :execrows
-- Снимает ключ, брошенный упавшим процессом: занят давно или его владелец
-- перестал отмечаться в api_instances (ADR 027).
DELETE FROM idempotency_keys k
WHERE k.scope = @scope AND k.key = @key AND k.completed_at IS NULL
  AND (k.created_at < @stale_before
       OR (k.owner IS NOT NULL AND NOT EXISTS (
             SELECT 1 FROM api_instances i
             WHERE i.id = k.owner AND i.seen_at > now() - make_interval(secs => @dead_after_secs::float8))));

-- name: Heartbeat :exec
INSERT INTO api_instances (id) VALUES (@id)
ON CONFLICT (id) DO UPDATE SET seen_at = now();

-- name: Leave :exec
DELETE FROM api_instances WHERE id = @id;

-- name: PruneInstances :exec
DELETE FROM api_instances WHERE seen_at < now() - interval '1 hour';
