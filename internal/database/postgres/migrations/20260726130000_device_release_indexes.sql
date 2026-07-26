-- +goose NO TRANSACTION
-- +goose Up

-- Indexes for the columns added by 20260726120000_device_release_columns.sql,
-- built CONCURRENTLY and therefore in their own migration: CREATE INDEX
-- CONCURRENTLY cannot run inside a transaction, and goose wraps a migration in
-- one unless the file opts out as this one does. Running after
-- 20260726125000_device_release_backfill.sql also means these are built once
-- over the final values instead of being maintained row by row through it.
--
-- It matters here specifically. device_identity is the hottest write path of
-- the registry: every check-in past the debounce bumps last_seen_at, which is
-- itself indexed, so no touch is ever a HOT update and every index is
-- maintained on every write. A plain CREATE INDEX would block those writes for
-- the whole build, which on a multi-million-row registry means blocking device
-- check-ins.
--
-- Deliberately not one index per filterable column. Indexed here are the two
-- dimensions a rollout is actually watched through ("how is my branch doing",
-- "who is on the release I just shipped") plus the two device columns selective
-- enough for the planner to prefer an index: 20260725100000_device_profile.sql
-- deferred those until a query filtered on them, and ListDevices,
-- CountOnlineDevices and the map all do now.
--
-- Left unindexed on purpose: platform, os_name and runtime_version. Each holds
-- a handful of distinct values per app, so a btree lookup returns a large
-- fraction of the rows and the planner falls back to the app_id-scoped scan
-- anyway. Add one the day a profile says otherwise, not before.
--
-- IF NOT EXISTS has a sharp edge with CONCURRENTLY: a build interrupted midway
-- leaves an INVALID index that this statement then skips. If a filter on these
-- columns is unexpectedly slow, check
--   SELECT indexrelid::regclass FROM pg_index WHERE NOT indisvalid;
-- and DROP INDEX CONCURRENTLY the invalid one before re-running.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_device_identity_branch
    ON device_identity (app_id, branch_name);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_device_identity_publish_group
    ON device_identity (app_id, publish_group);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_device_identity_device_model
    ON device_identity (app_id, device_model);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_device_identity_os_version
    ON device_identity (app_id, os_version);

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS idx_device_identity_os_version;
DROP INDEX CONCURRENTLY IF EXISTS idx_device_identity_device_model;
DROP INDEX CONCURRENTLY IF EXISTS idx_device_identity_publish_group;
DROP INDEX CONCURRENTLY IF EXISTS idx_device_identity_branch;
