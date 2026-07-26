-- +goose Up

-- Backfill of the columns added by 20260726120000_device_release_columns.sql,
-- from the joins being retired, so the fleet that already exists keeps its
-- release dimensions. It has to happen at all rather than lazily: a device that
-- never changes update never writes again, and would otherwise read as unknown
-- forever.
--
-- Its own migration, because goose wraps a migration in a transaction. Kept
-- next to the ALTER TABLE statements, this scan would run while their ACCESS
-- EXCLUSIVE lock is still held, which blocks reads as well as writes on the
-- hottest table of the registry for the whole duration of the backfill. Split,
-- the schema change commits immediately and this takes row locks only, on the
-- devices it actually resolves.
--
-- Giving up the atomicity of the two costs nothing. In between, the columns
-- read NULL, which the previous file already defines as "unknown", the same
-- value a device whose current_update_id never resolves keeps for good. A
-- backfill that fails rolls back whole and goose replays it on the next run.
--
-- Still a single statement over the matching devices, so on a multi-million-row
-- registry plan the deploy accordingly.
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

-- +goose Down

-- Nothing to undo. The columns this filled are dropped by the Down of
-- 20260726120000_device_release_columns.sql, which runs right after this one.
