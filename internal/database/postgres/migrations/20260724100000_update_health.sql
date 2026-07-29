-- +goose Up

-- The update each device is currently running, reported on every manifest
-- poll (Expo-Current-Update-ID = the launched update) and on telemetry
-- batches (expo.app.updates.id). NULL means no contact reported an id yet.
-- Devices on the embedded bundle report the embedded update's OWN id (the
-- manifest header always carries the launched update), so adoption cohorts
-- can contain ids that match no published update row; the dashboard labels
-- those as embedded/unknown. This is the instant-T adoption source of truth
-- for the WHOLE fleet, with no ClickHouse and no observe SDK required.
ALTER TABLE device_identity ADD COLUMN current_update_id UUID;
CREATE INDEX idx_device_identity_current_update ON device_identity (app_id, current_update_id);

-- Failures per (device, update), from two sources telling two different
-- stories:
--   'update_issue'  manifest error-recovery headers: the update crashed at
--                   launch (Expo-Recent-Failed-Update-IDs) and the device
--                   ROLLED BACK, so it is no longer counted in the update's
--                   current-device cohort.
--   'runtime_issue' the documented xprem_js_crash observe event: a
--                   JS crash while RUNNING the update. expo-updates never
--                   reports those, so the device keeps running the update
--                   and stays in its current-device cohort.
-- A device can later retry a rolled-back update and succeed (the server
-- does not exclude recently-failed updates from resolution), in which case
-- it counts once on each side of the health ratio: an attempt-level score,
-- deliberately.
-- failure_type is capture-once like fatal_error: the first recorded source
-- wins (health math never depends on the type, only display does; the
-- attempt accounting joins on device_identity.current_update_id instead).
-- fatal_error is capture-once: the client consumes Expo-Fatal-Error (sends
-- it exactly once, truncated to 1024 chars), so the first non-empty capture
-- wins and later sticky re-sends never blank it.
CREATE TABLE device_update_failures (
    app_id UUID NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    eas_client_id UUID NOT NULL,
    update_id UUID NOT NULL,
    failure_type TEXT NOT NULL CHECK (failure_type IN ('update_issue', 'runtime_issue')),
    fatal_error TEXT NOT NULL DEFAULT '',
    first_seen_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (app_id, eas_client_id, update_id)
);
-- Health of one update ("how many devices did X crash on") is the dominant
-- query; the PK serves the per-device view.
CREATE INDEX idx_device_update_failures_update ON device_update_failures (app_id, update_id);

-- +goose Down
DROP TABLE device_update_failures;
DROP INDEX idx_device_identity_current_update;
ALTER TABLE device_identity DROP COLUMN current_update_id;
