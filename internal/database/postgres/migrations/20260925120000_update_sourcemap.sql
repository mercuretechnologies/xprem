-- +goose Up
-- +goose StatementBegin

-- Hash of the bundle's source map in the sourcemap store. NULL: none uploaded.
ALTER TABLE updates ADD COLUMN sourcemap_hash TEXT;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE updates DROP COLUMN IF EXISTS sourcemap_hash;

-- +goose StatementEnd
