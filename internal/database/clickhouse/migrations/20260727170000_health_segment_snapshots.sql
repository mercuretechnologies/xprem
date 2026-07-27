-- +goose Up

-- The segmented health chart, precounted.
--
-- Reading it used to rebuild a device timeline on every page view: one row per
-- device per bucket, walked by two ASOF joins. Measured on a million devices,
-- a 24h window took 5.9s and seven days 9.2s, while the rows actually READ
-- stayed at 3.3 million. Nothing was scanned that should not be. The cost is
-- the grid, and the grid is the product of the fleet and the window.
--
-- So the grid gets built once per bucket by a worker instead of once per
-- window by a viewer. Same query, same source, one bucket at a time: measured
-- at 1.5s for a million devices with all updates and all eight dimensions
-- counted together, against 5.9s paid on every single read before.
--
-- Counted from device_health_events rather than from device_identity, which
-- carries these columns too. The event froze the device's model, country and
-- OS at the moment it adopted the update; device_identity holds what the
-- device is TODAY. Counting from the live table would have labelled a bucket
-- from last week with a value the device only took yesterday.
CREATE TABLE IF NOT EXISTS update_health_segment_snapshots (
    app_id            UUID,
    update_id         UUID,
    -- Which split this row belongs to ("country", "deviceModel"...) and the
    -- value inside it. Both LowCardinality: a deployment has eight dimensions
    -- and, per dimension, far fewer distinct values than it has devices.
    dimension         LowCardinality(String),
    -- Five minutes, the finest bucket any window offers (observeBucket). A
    -- coarser window rolls these up by taking the LAST sample it contains,
    -- never their sum: this counts devices present at an instant, and one
    -- device sits in every bucket it was alive for.
    bucket            DateTime('UTC'),
    segment           LowCardinality(String),
    captured_at       DateTime64(3, 'UTC'),
    devices_on_update UInt64,
    faulty_devices    UInt64
)
ENGINE = ReplacingMergeTree(captured_at)
PARTITION BY toYYYYMM(bucket)
-- dimension before bucket: a read asks for one dimension over a window, so the
-- prefix it seeks on is (app, update, dimension).
ORDER BY (app_id, update_id, dimension, bucket, segment);

-- +goose Down
DROP TABLE IF EXISTS update_health_segment_snapshots;
