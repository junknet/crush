-- name: CreateSession :one
INSERT INTO sessions (
    id,
    parent_session_id,
    title,
    mode,
    message_count,
    prompt_tokens,
    completion_tokens,
    cost,
    summary_message_id,
    working_dir,
    updated_at,
    created_at
) VALUES (
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    null,
    ?,
    strftime('%s', 'now'),
    strftime('%s', 'now')
) RETURNING id, parent_session_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at, summary_message_id, todos, mode, working_dir, last_prompt_tokens, last_completion_tokens, last_cache_creation_tokens, last_cache_read_tokens, last_context_pressure_tokens;

-- name: GetSessionByID :one
SELECT id, parent_session_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at, summary_message_id, todos, mode, working_dir, last_prompt_tokens, last_completion_tokens, last_cache_creation_tokens, last_cache_read_tokens, last_context_pressure_tokens
FROM sessions
WHERE id = ? LIMIT 1;

-- name: GetLastSession :one
SELECT id, parent_session_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at, summary_message_id, todos, mode, working_dir, last_prompt_tokens, last_completion_tokens, last_cache_creation_tokens, last_cache_read_tokens, last_context_pressure_tokens
FROM sessions
WHERE parent_session_id is NULL
  AND working_dir = ?
ORDER BY updated_at DESC
LIMIT 1;

-- name: ListSessions :many
SELECT id, parent_session_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at, summary_message_id, todos, mode, working_dir, last_prompt_tokens, last_completion_tokens, last_cache_creation_tokens, last_cache_read_tokens, last_context_pressure_tokens
FROM sessions
WHERE parent_session_id is NULL
  AND working_dir = ?
ORDER BY updated_at DESC;

-- name: UpdateSession :one
UPDATE sessions
SET
    title = ?,
    mode = ?,
    prompt_tokens = ?,
    completion_tokens = ?,
    last_prompt_tokens = ?,
    last_completion_tokens = ?,
    last_cache_creation_tokens = ?,
    last_cache_read_tokens = ?,
    last_context_pressure_tokens = ?,
    summary_message_id = ?,
    cost = ?,
    todos = ?
WHERE id = ?
RETURNING id, parent_session_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at, summary_message_id, todos, mode, working_dir, last_prompt_tokens, last_completion_tokens, last_cache_creation_tokens, last_cache_read_tokens, last_context_pressure_tokens;

-- name: UpdateSessionTitleAndUsage :exec
UPDATE sessions
SET
    title = ?,
    prompt_tokens = prompt_tokens + ?,
    completion_tokens = completion_tokens + ?,
    cost = cost + ?,
    updated_at = strftime('%s', 'now')
WHERE id = ?;


-- name: RenameSession :exec
UPDATE sessions
SET
    title = ?
WHERE id = ?;

-- name: DeleteSession :exec
DELETE FROM sessions
WHERE id = ?;
