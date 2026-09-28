-- name: Claim :execrows
-- Занимает ключ. 0 строк — ключ уже есть (выполнен, выполняется или брошен).
INSERT INTO idempotency_keys (scope, key, request_hash)
VALUES (@scope, @key, @request_hash)
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
-- Снимает ключ, брошенный упавшим процессом.
DELETE FROM idempotency_keys
WHERE scope = @scope AND key = @key AND completed_at IS NULL AND created_at < @stale_before;
