-- +goose Up

-- What each error of an update is, once one of its traces went through the
-- update's source map: the fingerprint it shares with the same error in
-- other updates, and the symbolicated trace shown for every occurrence.
-- Written by the error groups sweep (ee/observe/error_groups_job.go). A row
-- whose group_fingerprint is the zero UUID marks an error whose update has no
-- source map.
CREATE TABLE IF NOT EXISTS error_groups (
    app_id            UUID,
    update_id         UUID,
    error_fingerprint UUID,
    group_fingerprint UUID,
    error_type        LowCardinality(String),
    message           String,
    -- "LabScreen.tsx in onPress": the first frame of the app's own code.
    culprit           String,
    -- The symbolicated trace as JSON (symbolication.Trace).
    trace             String,
    symbolicated_at   DateTime('UTC'),
    INDEX idx_error_groups_group group_fingerprint TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = ReplacingMergeTree(symbolicated_at)
ORDER BY (app_id, update_id, error_fingerprint)
-- A row holds a whole trace, so a granule of the default 8192 rows would be
-- tens of megabytes read to answer for one error.
SETTINGS index_granularity = 256;

-- +goose Down
DROP TABLE IF EXISTS error_groups;
