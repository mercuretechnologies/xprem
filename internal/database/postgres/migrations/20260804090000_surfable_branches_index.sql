-- +goose NO TRANSACTION
-- +goose Up

-- Serves GetSurfableBranches, which every build carrying the branch picker runs
-- once per launch. The existing idx_updates_rv_branch_checked leads on
-- (runtime_version_id, branch_id) and carries neither platform nor created_at,
-- so the planner had to heap-fetch every checked update for the runtime version
-- — both platforms, all branches — and filter before aggregating. Leading with
-- the two equality predicates and carrying created_at makes it index-only.
DROP INDEX CONCURRENTLY IF EXISTS idx_updates_rv_platform_branch;
CREATE INDEX CONCURRENTLY idx_updates_rv_platform_branch
    ON updates (runtime_version_id, platform, branch_id, created_at DESC)
    WHERE checked_at IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_updates_rv_platform_branch;
