-- +goose Up
-- +goose StatementBegin

-- Shaped launch + assets (hash, key, contentType, ext) for this update.
-- NULL: pre-CAS row; compose falls back to the update-folder files.
ALTER TABLE updates ADD COLUMN asset_mapping JSONB;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE updates DROP COLUMN IF EXISTS asset_mapping;

-- +goose StatementEnd
