-- +goose Up

-- Country of the device at the time of the event, resolved from the request
-- IP at ingestion (GeoLite2, the same resolver the identity registry uses).
--
-- Denormalized rather than joined from device_identity on purpose. The
-- registry holds the CURRENT country of a device, which is a different fact:
-- a device that moves would retroactively rewrite where last month's slow
-- launches happened. Frozen on the row, "cold launch p90 in Brazil last week"
-- keeps meaning what it meant last week.
--
-- Rows ingested before this column exists keep the default empty string,
-- which every consumer must read as "not resolved", never as a country.
-- Low cardinality by nature (a couple hundred values at most).
ALTER TABLE observe_metrics
    ADD COLUMN IF NOT EXISTS country_code LowCardinality(String) DEFAULT '' AFTER device_model;

ALTER TABLE observe_logs
    ADD COLUMN IF NOT EXISTS country_code LowCardinality(String) DEFAULT '' AFTER device_model;

-- +goose Down

ALTER TABLE observe_logs DROP COLUMN IF EXISTS country_code;
ALTER TABLE observe_metrics DROP COLUMN IF EXISTS country_code;
