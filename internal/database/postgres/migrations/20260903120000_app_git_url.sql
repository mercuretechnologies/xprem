-- +goose Up

ALTER TABLE apps ADD COLUMN git_url VARCHAR(2048);

-- +goose Down

ALTER TABLE apps DROP COLUMN git_url;
