-- +goose Up

ALTER TABLE device_identity ADD COLUMN IF NOT EXISTS channel_name TEXT;

-- +goose Down

ALTER TABLE device_identity DROP COLUMN IF EXISTS channel_name;
