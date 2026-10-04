-- +goose Up

-- The fingerprint of an error log (ee/observe/fingerprint.go). The zero UUID
-- marks a log that is not an error.
ALTER TABLE observe_logs
    ADD COLUMN IF NOT EXISTS error_fingerprint UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000');

-- How often each error of an update happened, counted per hour of ingestion
-- as the logs arrive, so an error a device sends late still counts as recent.
-- Reading the errors of an update reads these rows, never the logs.
-- A batch the SDK sends twice is counted twice: the logs table only drops
-- such duplicates at read time, through content_key.
CREATE TABLE IF NOT EXISTS error_occurrences (
    app_id            UUID,
    update_id         UUID,
    error_fingerprint UUID,
    hour              DateTime('UTC'),
    -- The type and message of one occurrence, to name the error.
    title             SimpleAggregateFunction(any, String),
    occurrences       SimpleAggregateFunction(sum, UInt64),
    crashes           SimpleAggregateFunction(sum, UInt64),
    devices           AggregateFunction(uniq, UUID),
    first_seen        SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen         SimpleAggregateFunction(max, DateTime64(9, 'UTC'))
)
ENGINE = AggregatingMergeTree
-- By day, not by month: the sweep asks for the last 24 hours every minute,
-- and hour is last in the key, so partitions are all that spare it a scan
-- of the whole month. A few hundred rows a day keeps the parts small.
PARTITION BY toYYYYMMDD(hour)
ORDER BY (app_id, update_id, error_fingerprint, hour);

CREATE MATERIALIZED VIEW IF NOT EXISTS error_occurrences_mv TO error_occurrences AS
SELECT
    app_id,
    update_id,
    error_fingerprint,
    toStartOfHour(ingested_at) AS hour,
    -- The record's type and message under whichever keys it uses, as
    -- ee/observe/fingerprint.go reads them: the SDK's exception.*, the manual
    -- crash event's name and message, else the body, else the event name.
    any(arrayStringConcat(arrayFilter(part -> part != '', [
        if(JSONExtractString(attributes, 'exception.type') != '',
           JSONExtractString(attributes, 'exception.type'), JSONExtractString(attributes, 'name')),
        multiIf(JSONExtractString(attributes, 'exception.message') != '', JSONExtractString(attributes, 'exception.message'),
                JSONExtractString(attributes, 'message') != '', JSONExtractString(attributes, 'message'),
                body != '', body,
                event_name)
    ]), ': ')) AS title,
    count() AS occurrences,
    countIf(is_fatal = 1) AS crashes,
    uniqState(eas_client_id) AS devices,
    min(timestamp) AS first_seen,
    max(timestamp) AS last_seen
FROM observe_logs
WHERE error_fingerprint != toUUID('00000000-0000-0000-0000-000000000000')
GROUP BY app_id, update_id, error_fingerprint, hour;

-- +goose Down
DROP VIEW IF EXISTS error_occurrences_mv;
DROP TABLE IF EXISTS error_occurrences;
ALTER TABLE observe_logs DROP COLUMN IF EXISTS error_fingerprint;
