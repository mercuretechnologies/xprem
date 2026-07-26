-- +goose Up

-- One row per (app, device, update, failure_type) instead of one per
-- (app, device, update). The two failure sources describe different events on
-- the same pair and must not share a row.
--
-- What sharing did. The key carried no failure_type and neither writer set it
-- on conflict, so whichever source landed first owned the type forever. A JS
-- crash followed by a launch rollback left the rollback typed 'runtime_issue',
-- and a later successful start then resolved it through
-- ResolveDeviceRuntimeFailure, erasing durable evidence of a native rollback.
-- The other order was just as wrong: a rollback followed by a JS crash kept the
-- row 'update_issue', so the crash was invisible as a runtime issue and no
-- successful start could ever resolve it, leaving the device faulty for good.
-- Both showed up as wrong updateIssues/runtimeIssues figures on the dashboard.
--
-- Widening cannot create duplicates: the old key already allowed exactly one
-- row per pair, so every existing row keeps its identity under the new one.
--
-- Rebuilding a primary key takes ACCESS EXCLUSIVE for its duration. The table
-- holds one row per failed (device, update), not one per device, so it is
-- orders of magnitude smaller than device_identity; on a registry where that is
-- not true, run this in a maintenance window.
ALTER TABLE device_update_failures
    DROP CONSTRAINT device_update_failures_pkey;
ALTER TABLE device_update_failures
    ADD CONSTRAINT device_update_failures_pkey
    PRIMARY KEY (app_id, eas_client_id, update_id, failure_type);

-- +goose Down

-- Collapsing back to one row per pair has to drop the losers: keep the
-- earliest-seen row per (app, device, update), which is the one the old key
-- would have retained since the first writer owned it.
DELETE FROM device_update_failures f
USING device_update_failures keep
WHERE f.app_id = keep.app_id
  AND f.eas_client_id = keep.eas_client_id
  AND f.update_id = keep.update_id
  AND (keep.first_seen_at, keep.failure_type) < (f.first_seen_at, f.failure_type);

ALTER TABLE device_update_failures
    DROP CONSTRAINT device_update_failures_pkey;
ALTER TABLE device_update_failures
    ADD CONSTRAINT device_update_failures_pkey
    PRIMARY KEY (app_id, eas_client_id, update_id);
