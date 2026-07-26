-- +goose Up

-- The store version a device runs, alongside the hardware columns added by
-- 20260725100000_device_profile.sql and written under the same rules: only
-- expo-observe reports it, a manifest poll passes NULL, and NULL means "not
-- reported" rather than a population.
--
-- It is here rather than only on the ClickHouse telemetry rows because the
-- outbox resolves an event's dimensions from this table, and segmented health
-- history reads them from the event. Left out, app_version would be the one
-- segment whose value had to come from a query-time lookup of what the device
-- runs TODAY, which relabels a device's whole history the moment it takes a
-- store update: exactly the release window the chart exists to watch.
ALTER TABLE device_identity ADD COLUMN IF NOT EXISTS app_version TEXT;

-- No index, same reasoning as the hardware columns: this is the registry's
-- hottest write path and nothing filters on it yet.

-- +goose Down

ALTER TABLE device_identity DROP COLUMN IF EXISTS app_version;
