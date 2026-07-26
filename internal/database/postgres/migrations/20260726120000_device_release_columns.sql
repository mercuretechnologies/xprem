-- +goose Up

-- The release dimensions of whatever update a device currently runs, stored on
-- the device instead of joined from it. Same reasoning as the ClickHouse side
-- (see 20260725140000_observe_update_group.sql): an update never changes
-- branch, runtime version, platform or publish group, so there is nothing to
-- keep in sync. The registry was the last place still resolving them at read
-- time.
--
-- What it buys, measured on the queries that carried the joins (numbers from
-- the comments on CountOnlineDevices, 2.4M online devices): the three-way
-- LEFT JOIN plus its EXISTS cost 586ms unfiltered and 1025ms with a branch
-- filter, against 45ms and 112ms without. Five call sites paid it or worked
-- around it: ListDevices and CountObserveActiveDevices carried the joins,
-- CountOnlineDevices replaced them with a hashed subquery, and the map kept
-- two parallel queries (ListObserveLocations / ...ByRelease) chosen at runtime
-- purely to avoid them.
--
-- The app-scoping guard moves with the data. current_update_id arrives on the
-- unauthenticated wire, so a device of app A can claim an update of app B; the
-- reads used an EXISTS on the join to refuse that claim. Resolution now happens
-- once on write, scoped to the app, and a claim that does not resolve leaves
-- these columns NULL, which is exactly what the LEFT JOIN produced.
--
-- NULL means "unknown", never "none": a device on the embedded bundle, one
-- running an update this server never published, and one whose check-in never
-- carried the header all read the same way.
ALTER TABLE device_identity ADD COLUMN IF NOT EXISTS branch_name TEXT;
ALTER TABLE device_identity ADD COLUMN IF NOT EXISTS runtime_version TEXT;
ALTER TABLE device_identity ADD COLUMN IF NOT EXISTS platform TEXT;
ALTER TABLE device_identity ADD COLUMN IF NOT EXISTS publish_group UUID;

-- Backfill from the joins being retired, so the existing fleet keeps its
-- release dimensions. It has to happen here rather than lazily: a device that
-- never changes update never writes again, and would otherwise read as unknown
-- forever. One statement, so it takes a write lock on device_identity for its
-- duration; on a multi-million-row registry plan the deploy accordingly.
UPDATE device_identity d SET
    branch_name = o.branch_name,
    runtime_version = o.runtime_version,
    platform = o.platform,
    publish_group = o.publish_group
FROM (
    SELECT u.update_uuid,
           b.app_id,
           b.name AS branch_name,
           rv.version AS runtime_version,
           u.platform,
           u.publish_group
    FROM updates u
    INNER JOIN branches b ON b.id = u.branch_id
    LEFT JOIN runtime_versions rv ON rv.id = u.runtime_version_id
    WHERE u.update_uuid IS NOT NULL
) o
WHERE d.current_update_id = o.update_uuid
  AND d.app_id = o.app_id;

-- Indexes, deliberately not one per filterable column. device_identity is the
-- hottest write path of the registry: every check-in past the debounce bumps
-- last_seen_at, which is itself indexed, so no touch is ever a HOT update and
-- every index is maintained on every write. Each one added is paid on all of
-- them.
--
-- Indexed here are the two dimensions a rollout is actually watched through
-- ("how is my branch doing", "who is on the release I just shipped") plus the
-- two device columns selective enough for the planner to prefer an index:
-- 20260725100000_device_profile.sql deferred these until a query filtered on
-- them, and ListDevices, CountOnlineDevices and the map all do now.
--
-- Left unindexed on purpose: platform, os_name and runtime_version. Each holds
-- a handful of distinct values per app, so a btree lookup returns a large
-- fraction of the rows and the planner falls back to the app_id-scoped scan
-- anyway. Add one the day a profile says otherwise, not before.
CREATE INDEX IF NOT EXISTS idx_device_identity_branch ON device_identity (app_id, branch_name);
CREATE INDEX IF NOT EXISTS idx_device_identity_publish_group ON device_identity (app_id, publish_group);
CREATE INDEX IF NOT EXISTS idx_device_identity_device_model ON device_identity (app_id, device_model);
CREATE INDEX IF NOT EXISTS idx_device_identity_os_version ON device_identity (app_id, os_version);

-- +goose Down

DROP INDEX IF EXISTS idx_device_identity_os_version;
DROP INDEX IF EXISTS idx_device_identity_device_model;
DROP INDEX IF EXISTS idx_device_identity_publish_group;
DROP INDEX IF EXISTS idx_device_identity_branch;

ALTER TABLE device_identity DROP COLUMN IF EXISTS publish_group;
ALTER TABLE device_identity DROP COLUMN IF EXISTS platform;
ALTER TABLE device_identity DROP COLUMN IF EXISTS runtime_version;
ALTER TABLE device_identity DROP COLUMN IF EXISTS branch_name;
