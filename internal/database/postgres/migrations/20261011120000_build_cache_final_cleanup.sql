-- +goose Up
ALTER TABLE build_cache_cleanup
ADD COLUMN final_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '100 minutes';

-- +goose Down
ALTER TABLE build_cache_cleanup DROP COLUMN final_at;
