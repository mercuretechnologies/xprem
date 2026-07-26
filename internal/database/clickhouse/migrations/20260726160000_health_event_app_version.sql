-- +goose Up

-- The last dimension the segmented health history reads from the event rather
-- than from a query-time lookup of what the device runs today. The other seven
-- arrived with 20260725180000_health_event_dimensions.sql; app_version could
-- not follow then because the registry did not store it, which
-- 20260726150000_device_app_version.sql fixes on the PostgreSQL side.
--
-- Rows delivered before this migration keep the empty string, which reads as
-- "unknown" rather than pretending to a version.
ALTER TABLE device_health_events
    ADD COLUMN IF NOT EXISTS app_version LowCardinality(String) DEFAULT '' AFTER country_code;

-- +goose Down
ALTER TABLE device_health_events DROP COLUMN IF EXISTS app_version;
