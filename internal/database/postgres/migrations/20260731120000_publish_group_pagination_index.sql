-- +goose NO TRANSACTION
-- +goose Up

-- Lets update and publish-group pages seek directly within one branch/runtime
-- in newest-id order. publish_group is included for the representative-row
-- check without widening the ordering key; idx_updates_publish_group still
-- serves member reads.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_updates_pagination
    ON updates(branch_id, runtime_version_id, id DESC)
    INCLUDE (publish_group)
    WHERE checked_at IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_updates_pagination;
