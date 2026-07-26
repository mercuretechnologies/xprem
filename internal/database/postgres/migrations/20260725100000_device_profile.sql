-- +goose Up

-- Hardware and OS of each registered device. They already ride on every
-- telemetry row in ClickHouse, where they answer "is this update slow on old
-- phones". Here they answer a different question: describing a device in the
-- registry, so a crash or a cohort can be read next to what it runs on
-- without a ClickHouse round trip.
--
-- Only expo-observe fills these. The manifest path carries no such headers
-- (expo-platform, expo-runtime-version, expo-channel-name and the client id
-- are all it sends), so a device that never ships telemetry keeps them NULL.
-- Any query grouping on them must therefore treat NULL as "not reported"
-- rather than as a population.
--
-- COALESCE semantics on write, exactly like the geo columns: a check-in that
-- does not know the hardware (every manifest poll) must never erase what a
-- previous telemetry batch established.
ALTER TABLE device_identity ADD COLUMN IF NOT EXISTS device_model TEXT;
ALTER TABLE device_identity ADD COLUMN IF NOT EXISTS os_name TEXT;
ALTER TABLE device_identity ADD COLUMN IF NOT EXISTS os_version TEXT;

-- No index yet on purpose. device_identity is the hottest write path of the
-- registry (one row per device, touched by every poll past the debounce) and
-- nothing reads these columns as a predicate yet. Add one alongside the first
-- query that filters on them, not before.

-- +goose Down

ALTER TABLE device_identity DROP COLUMN IF EXISTS os_version;
ALTER TABLE device_identity DROP COLUMN IF EXISTS os_name;
ALTER TABLE device_identity DROP COLUMN IF EXISTS device_model;
