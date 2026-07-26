-- +goose Up

-- Coordinates of the device at the time of the event, resolved from the request
-- IP at ingestion by the same GeoLite2 City database the identity registry
-- uses. They are the city centroid GeoLite2 returns, never an exact position.
--
-- Nothing reads these yet. They are stored now because they cannot be
-- recovered later: the request IP is not retained, so a column added in six
-- months starts with six months of holes. Filtering today goes through
-- country_code, which is cheap, groupable and already wired; keeping the
-- coordinates leaves the door open to answering "metrics inside this area"
-- geometrically without a migration that can only cover the future.
--
-- Frozen on the row for the same reason country_code is: the registry holds
-- where a device is NOW, which would retroactively move last month's slow
-- launches to wherever that phone has since travelled.
--
-- Float64 to match the type the values already have: GeoLite2 hands back
-- float64, the identity registry stores float64, and the insert binds *float64.
-- The driver converts nothing here, so a narrower column would reject every
-- batch rather than round it (clickhouse-go refuses *float64 -> Float32, null
-- pointers included). The extra four bytes buy precision nobody needs on a city
-- centroid; they buy an insert that works.
--
-- Nullable rather than a sentinel because (0, 0) is a real place in the Gulf of
-- Guinea, so any in-band "unknown" would be a lie. Expect them to be null
-- often: GeoLite2 resolves an address block to a country long before it
-- resolves it to a city.
ALTER TABLE observe_metrics
    ADD COLUMN IF NOT EXISTS lat Nullable(Float64) AFTER country_code,
    ADD COLUMN IF NOT EXISTS lng Nullable(Float64) AFTER lat;

ALTER TABLE observe_logs
    ADD COLUMN IF NOT EXISTS lat Nullable(Float64) AFTER country_code,
    ADD COLUMN IF NOT EXISTS lng Nullable(Float64) AFTER lat;

-- +goose Down

ALTER TABLE observe_logs DROP COLUMN IF EXISTS lng, DROP COLUMN IF EXISTS lat;
ALTER TABLE observe_metrics DROP COLUMN IF EXISTS lng, DROP COLUMN IF EXISTS lat;
