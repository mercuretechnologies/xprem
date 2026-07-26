-- +goose Up

-- The publish an update belongs to. eoas mints one identifier per publish run
-- and sends it with each per-platform call, so "my last release" is a group of
-- one or two updates, never a single row.
--
-- Denormalized like the branch, and for the same reason: an update never
-- changes group, so there is nothing to keep in sync, and grouping a metric by
-- publish becomes a plain GROUP BY instead of a join against Postgres.
--
-- The zero UUID means "no group": updates published by an older CLI, rollback
-- markers, and every row ingested before this column existed. Consumers must
-- read it as "ungrouped", never as a real group.
ALTER TABLE observe_metrics
    ADD COLUMN IF NOT EXISTS update_group_id UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000') AFTER update_id;

ALTER TABLE observe_logs
    ADD COLUMN IF NOT EXISTS update_group_id UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000') AFTER update_id;

-- +goose Down

ALTER TABLE observe_logs DROP COLUMN IF EXISTS update_group_id;
ALTER TABLE observe_metrics DROP COLUMN IF EXISTS update_group_id;
