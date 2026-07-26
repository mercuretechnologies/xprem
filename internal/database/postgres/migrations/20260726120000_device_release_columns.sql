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

-- The backfill of the existing fleet and the indexes these columns need each
-- live in their own migration (20260726125000_device_release_backfill.sql and
-- 20260726130000_device_release_indexes.sql), for the same reason in two forms:
-- goose wraps a migration in a transaction, so anything kept here would run
-- while the ACCESS EXCLUSIVE lock these statements take is still held, and
-- CREATE INDEX CONCURRENTLY cannot run inside a transaction at all. Adding a
-- nullable column with no default is a catalog-only change, so alone this file
-- commits in milliseconds and releases that lock immediately.
--
-- Until those two run, the columns read NULL and a filter on them falls back to
-- the app_id-scoped scan: slower than the final state, never wrong.

-- +goose Down

ALTER TABLE device_identity DROP COLUMN IF EXISTS publish_group;
ALTER TABLE device_identity DROP COLUMN IF EXISTS platform;
ALTER TABLE device_identity DROP COLUMN IF EXISTS runtime_version;
ALTER TABLE device_identity DROP COLUMN IF EXISTS branch_name;
