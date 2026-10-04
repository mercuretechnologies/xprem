-- +goose NO TRANSACTION
-- +goose Up

-- Supports the per-branch/runtime/platform serving-head order used by release
-- and adoption reads.
DROP INDEX CONCURRENTLY IF EXISTS idx_updates_serving_heads;
CREATE INDEX CONCURRENTLY idx_updates_serving_heads
    ON updates(branch_id, runtime_version_id, platform, id DESC)
    WHERE checked_at IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_updates_serving_heads;
