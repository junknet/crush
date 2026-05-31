-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN last_prompt_tokens INTEGER NOT NULL DEFAULT 0 CHECK (last_prompt_tokens >= 0);
ALTER TABLE sessions ADD COLUMN last_completion_tokens INTEGER NOT NULL DEFAULT 0 CHECK (last_completion_tokens >= 0);
ALTER TABLE sessions ADD COLUMN last_cache_creation_tokens INTEGER NOT NULL DEFAULT 0 CHECK (last_cache_creation_tokens >= 0);
ALTER TABLE sessions ADD COLUMN last_cache_read_tokens INTEGER NOT NULL DEFAULT 0 CHECK (last_cache_read_tokens >= 0);
ALTER TABLE sessions ADD COLUMN last_context_pressure_tokens INTEGER NOT NULL DEFAULT 0 CHECK (last_context_pressure_tokens >= 0);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- SQLite cannot drop a column without rebuilding the table.
-- +goose StatementEnd
