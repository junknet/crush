-- name: GetMessage :one
SELECT *
FROM messages
WHERE id = ? LIMIT 1;

-- name: ListMessagesBySession :many
SELECT *
FROM messages
WHERE session_id = ?
ORDER BY created_at ASC, rowid ASC;

-- name: CreateMessage :one
INSERT INTO messages (
    id,
    session_id,
    role,
    parts,
    model,
    provider,
    is_summary_message,
    created_at,
    updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now')
)
RETURNING *;

-- name: UpdateMessage :exec
UPDATE messages
SET
    parts = ?,
    finished_at = ?,
    updated_at = strftime('%s', 'now')
WHERE id = ?;


-- name: DeleteMessage :exec
DELETE FROM messages
WHERE id = ?;

-- name: DeleteSessionMessages :exec
DELETE FROM messages
WHERE session_id = ?;

-- name: ListUserMessagesBySession :many
SELECT *
FROM messages
WHERE session_id = ? AND role = 'user'
ORDER BY created_at DESC;

-- name: ListAllUserMessages :many
SELECT *
FROM messages
WHERE role = 'user'
ORDER BY created_at DESC;

-- name: ListMessagesBySessionBefore :many
-- Keyset page of messages strictly older than the (created_at, id) cursor, in
-- descending order. (created_at, id) is a deterministic total order so paging
-- never skips, and with client-side map dedupe never duplicates either.
SELECT *
FROM messages
WHERE session_id = @session_id
  AND (created_at < @before_created_at OR (created_at = @before_created_at AND id < @before_id))
ORDER BY created_at DESC, id DESC
LIMIT @row_limit;
