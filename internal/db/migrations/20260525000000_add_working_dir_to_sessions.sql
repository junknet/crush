-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN working_dir TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- SQLite cannot drop a column without rebuilding the table.
-- +goose StatementEnd
