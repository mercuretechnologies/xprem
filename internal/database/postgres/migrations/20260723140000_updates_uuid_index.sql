-- +goose NO TRANSACTION
-- +goose Up
-- The observe branch resolver looks updates up by their uuid (once per
-- distinct update thanks to its cache, but a cold cache after boot walks many
-- distinct ids). The primary key is (branch_id, id): without this index every
-- lookup is a sequential scan.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_updates_update_uuid ON updates (update_uuid);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_updates_update_uuid;
