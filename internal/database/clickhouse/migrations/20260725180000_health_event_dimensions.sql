-- +goose Up

-- Native launch crashes never reach the SDK: the app dies before it runs, so
-- the only witness is the manifest poll that follows. They therefore live in
-- device_health_events rather than observe_logs, and until now they carried
-- nothing but ids.
--
-- The Logs view shows them alongside the records the app did send, and a log
-- list answers to branch, platform and hardware filters. A row that cannot be
-- filtered would either have to be hidden whenever a filter is on (a crash
-- that silently disappears) or shown against a filter it contradicts. So the
-- dimensions are denormalized here at delivery, exactly as the ingestion path
-- does for telemetry, and for the same reason: an update never changes branch
-- and a crash never changes device.
--
-- Rows delivered before this migration keep empty strings, which read as
-- "unknown" and drop out of a filtered view rather than pretending.
ALTER TABLE device_health_events
    ADD COLUMN IF NOT EXISTS branch LowCardinality(String) DEFAULT '' AFTER update_id;
ALTER TABLE device_health_events
    ADD COLUMN IF NOT EXISTS runtime_version LowCardinality(String) DEFAULT '' AFTER branch;
ALTER TABLE device_health_events
    ADD COLUMN IF NOT EXISTS platform LowCardinality(String) DEFAULT '' AFTER runtime_version;
ALTER TABLE device_health_events
    ADD COLUMN IF NOT EXISTS os_name LowCardinality(String) DEFAULT '' AFTER platform;
ALTER TABLE device_health_events
    ADD COLUMN IF NOT EXISTS os_version LowCardinality(String) DEFAULT '' AFTER os_name;
ALTER TABLE device_health_events
    ADD COLUMN IF NOT EXISTS device_model LowCardinality(String) DEFAULT '' AFTER os_version;
ALTER TABLE device_health_events
    ADD COLUMN IF NOT EXISTS country_code LowCardinality(String) DEFAULT '' AFTER device_model;

-- +goose Down
ALTER TABLE device_health_events DROP COLUMN IF EXISTS country_code;
ALTER TABLE device_health_events DROP COLUMN IF EXISTS device_model;
ALTER TABLE device_health_events DROP COLUMN IF EXISTS os_version;
ALTER TABLE device_health_events DROP COLUMN IF EXISTS os_name;
ALTER TABLE device_health_events DROP COLUMN IF EXISTS platform;
ALTER TABLE device_health_events DROP COLUMN IF EXISTS runtime_version;
ALTER TABLE device_health_events DROP COLUMN IF EXISTS branch;
