-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN mode TEXT NOT NULL DEFAULT 'execute';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- SQLite cannot drop a column without rebuilding the table.
-- +goose StatementEnd
