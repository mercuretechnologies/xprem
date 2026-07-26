-- +goose Up

-- When the update named by current_update_id was OBSERVED, which is not when
-- the row was written. A device that takes an update while offline flushes the
-- telemetry backlog it recorded BEFORE the switch, so that batch and the
-- manifest poll announcing the new update race: both miss the check-in
-- debounce (a real state change is exactly what the debounce lets through),
-- both write, and whichever lands last wins. Without this column the older
-- observation could overwrite the newer one, and the trigger on
-- current_update_id would then emit a 'switched' event back to the update the
-- device had already left, which is permanent in the analytical store.
--
-- last_seen_at cannot serve: it means "when we last heard from this device",
-- which for a backlog flush is now, while the observation it carries is old.
--
-- NULL on every existing row, which reads as "never observed" and lets the
-- first check-in after this migration write unconditionally.
ALTER TABLE device_identity ADD COLUMN IF NOT EXISTS current_update_observed_at TIMESTAMP WITH TIME ZONE;

-- +goose Down

ALTER TABLE device_identity DROP COLUMN IF EXISTS current_update_observed_at;
