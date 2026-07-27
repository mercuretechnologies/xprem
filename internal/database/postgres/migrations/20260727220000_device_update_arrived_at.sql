-- +goose Up

-- When the device ARRIVED on the update it runs, as opposed to when we last
-- heard from it.
--
-- current_update_observed_at, the column next to this one, was written on every
-- poll that named an update, whether or not the update had changed. That is
-- what it is for: it orders racing observations, so a delayed poll cannot
-- overwrite a newer state. It is therefore a last-seen watermark, and reading
-- it as an arrival dated every device at its most recent check-in. A fleet that
-- had been on one update for a week charted as having adopted it in the last
-- few minutes, and the curve moved again on every reload.
--
-- A column of its own rather than narrowing the other one: making
-- current_update_observed_at stand still while a device stays put would have
-- widened the window in which a delayed poll counts as fresh, trading a wrong
-- chart for a wrong state.
ALTER TABLE device_identity
    ADD COLUMN IF NOT EXISTS current_update_arrived_at TIMESTAMP WITH TIME ZONE;

-- No backfill, deliberately, and not for lack of a source.
--
-- The obvious one is current_update_observed_at, but that column was itself
-- added without one (20260726170000), so it is NULL on every row written before
-- it and a backfill from it would match nothing on any real upgrade while still
-- paying a full scan of the registry. Worse, goose wraps a migration in a
-- transaction, so that scan would run under the ADD COLUMN's ACCESS EXCLUSIVE
-- lock and block reads as well as writes on the hottest table of the product,
-- which is exactly what 20260726125000_device_release_backfill.sql was split
-- out to avoid.
--
-- A NULL arrival is not a device lost, either: the only reader folds it into
-- the start of whatever window it is drawing, which is true of every row this
-- column predates. Devices correct themselves the next time they move.

-- +goose Down

ALTER TABLE device_identity DROP COLUMN IF EXISTS current_update_arrived_at;
