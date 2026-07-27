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

-- Seeded from the watermark, which is the best available answer for rows that
-- predate this column: it is right for every device that has not polled since
-- it arrived, and too recent for the others. Those correct themselves on their
-- next move.
UPDATE device_identity
SET current_update_arrived_at = current_update_observed_at
WHERE current_update_arrived_at IS NULL
  AND current_update_observed_at IS NOT NULL;

-- +goose Down

ALTER TABLE device_identity DROP COLUMN IF EXISTS current_update_arrived_at;
