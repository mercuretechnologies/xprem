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

-- The indexes these columns need are built CONCURRENTLY in the migration that
-- follows this one, which cannot share a transaction with the schema change
-- above. Until it runs, a filter on the new columns falls back to the
-- app_id-scoped scan: slower than the final state, never wrong.

-- +goose Down

ALTER TABLE device_identity DROP COLUMN IF EXISTS publish_group;
ALTER TABLE device_identity DROP COLUMN IF EXISTS platform;
ALTER TABLE device_identity DROP COLUMN IF EXISTS runtime_version;
ALTER TABLE device_identity DROP COLUMN IF EXISTS branch_name;
