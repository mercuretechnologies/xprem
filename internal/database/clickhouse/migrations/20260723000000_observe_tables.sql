-- +goose Up

-- The Observe fact tables. Two sources feed them:
--   Source A: the expo-observe SDK (55+) POSTing OTLP JSON to
--             /observe/{APP_ID}/{projectId}/v1/metrics and /v1/logs.
--             SDK 55 sends startup/update metrics only; logs, JS errors,
--             navigation and the richer update dimensions require SDK 56.
--   Source B: headers every expo-updates client already sends on /manifest
--             (Expo-Current-Update-ID, Expo-Recent-Failed-Update-IDs,
--             Expo-Fatal-Error), which carry adoption and launch-crash
--             signals for updates whether or not the app ships the SDK.
--
-- update_id is the primary analytical filter, so it sits in every sorting
-- key. It is non-nullable on purpose: ClickHouse rejects Nullable columns in
-- a sorting key, so the zero UUID (00000000-0000-0000-0000-000000000000)
-- means "running the embedded bundle, no OTA update".
--
-- branch is denormalized at ingestion (derived from update_id, never from
-- the channel: a channel can be re-pointed to another branch over time,
-- update->branch is permanent). The mutable identity dimension (metadata,
-- geo) deliberately stays in Postgres; cohort filters resolve device id
-- lists there and land here as eas_client_id predicates, hence the bloom
-- filter indexes.

-- One row per OTLP gauge data point, resource attributes flattened onto the
-- row so every query filters on plain columns.
CREATE TABLE IF NOT EXISTS observe_metrics (
    app_id          UUID,
    eas_client_id   UUID,
    update_id       UUID,
    branch          LowCardinality(String),
    channel         LowCardinality(String),
    runtime_version LowCardinality(String),
    platform        LowCardinality(String),
    session_id      UUID,
    metric_name     LowCardinality(String),
    value           Float64,
    route_name      String DEFAULT '',
    -- expo.custom_params arrives as a JSON string attribute; kept verbatim.
    custom_params   String DEFAULT '',
    os_name         LowCardinality(String),
    os_version      LowCardinality(String),
    device_model    LowCardinality(String) DEFAULT '',
    app_version     LowCardinality(String),
    sdk_version     LowCardinality(String),
    timestamp       DateTime64(9, 'UTC'),
    ingested_at     DateTime('UTC') DEFAULT now(),
    INDEX idx_metrics_client eas_client_id TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(timestamp)
ORDER BY (app_id, update_id, timestamp);

-- One row per OTLP log record. event.name = 'exception' rows are the JS
-- crashes (runtime crashes of updates that DID launch; launch crashes never
-- reach the SDK and live in update_crashes below).
CREATE TABLE IF NOT EXISTS observe_logs (
    app_id          UUID,
    eas_client_id   UUID,
    update_id       UUID,
    branch          LowCardinality(String),
    channel         LowCardinality(String),
    runtime_version LowCardinality(String),
    platform        LowCardinality(String),
    session_id      UUID,
    event_name      LowCardinality(String),
    severity_number UInt8,
    severity_text   LowCardinality(String),
    -- from the expo.error.is_fatal attribute on exception records.
    is_fatal        UInt8 DEFAULT 0,
    body            String DEFAULT '',
    -- remaining record attributes as a JSON string (exception.type,
    -- exception.message, exception.stacktrace, user attributes...).
    attributes      String DEFAULT '',
    os_name         LowCardinality(String),
    os_version      LowCardinality(String),
    device_model    LowCardinality(String) DEFAULT '',
    app_version     LowCardinality(String),
    sdk_version     LowCardinality(String),
    timestamp       DateTime64(9, 'UTC'),
    ingested_at     DateTime('UTC') DEFAULT now(),
    INDEX idx_logs_client eas_client_id TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(timestamp)
ORDER BY (app_id, update_id, timestamp);

-- Source B, adoption: the update each device is currently running, from
-- Expo-Current-Update-ID. Written only when the value CHANGES for a device
-- (an in-process cache filters repeat polls); the ReplacingMergeTree key
-- makes re-emissions after a server restart collapse instead of duplicating.
-- An update that crashes on launch is rolled back before it ever becomes
-- launchedUpdate, so it never appears here: that is what makes "zero rows in
-- device_current_update + rows in update_crashes" the signature of a toxic
-- update.
CREATE TABLE IF NOT EXISTS device_current_update (
    app_id            UUID,
    eas_client_id     UUID,
    current_update_id UUID,
    branch            LowCardinality(String),
    channel           LowCardinality(String),
    runtime_version   LowCardinality(String),
    platform          LowCardinality(String),
    -- Millisecond precision: this is the replace version, and two replicas
    -- observing a device transition within the same second must not tie
    -- (a tie keeps arrival order, not event order).
    changed_at        DateTime64(3, 'UTC')
)
ENGINE = ReplacingMergeTree(changed_at)
ORDER BY (app_id, eas_client_id);

-- Source B, launch crashes: one row per (device, update) that appeared in
-- Expo-Recent-Failed-Update-IDs. The key collapses the sticky header (the
-- failed list is re-sent on every poll) into a single crash per pair.
-- fatal_error is captured from Expo-Fatal-Error on the first poll after the
-- crash; the client consumes it (sends it exactly once, truncated to 1024
-- chars), so a missed capture leaves '' and only the crash fact remains.
--
-- AggregatingMergeTree, NOT ReplacingMergeTree: after a server restart the
-- change-detection cache is cold, so the sticky header re-emits a row with
-- fatal_error '' and a newer timestamp; a replace would keep that whole row
-- and destroy the captured error (there is no per-column replace). Per-column
-- aggregation merges order-independently with no read-before-write: max keeps
-- the error ('' sorts below any non-empty string), min/max keep the true seen
-- window. The dimension columns stay plain: constant per key, any survivor is
-- correct. Read with GROUP BY min/max (or FINAL).
CREATE TABLE IF NOT EXISTS update_crashes (
    app_id          UUID,
    eas_client_id   UUID,
    update_id       UUID,
    fatal_error     SimpleAggregateFunction(max, String),
    branch          LowCardinality(String),
    channel         LowCardinality(String),
    runtime_version LowCardinality(String),
    platform        LowCardinality(String),
    first_seen_at   SimpleAggregateFunction(min, DateTime('UTC')),
    last_seen_at    SimpleAggregateFunction(max, DateTime('UTC'))
)
ENGINE = AggregatingMergeTree
-- update_id before eas_client_id: the dominant query is one update's health
-- ("how many devices did X crash on"), a key prefix this way. Dedup only
-- depends on the key SET, not its order, and the per-device crash view stays
-- cheap on a table this small (one row per crashed pair).
ORDER BY (app_id, update_id, eas_client_id);

-- +goose Down
DROP TABLE IF EXISTS update_crashes;
DROP TABLE IF EXISTS device_current_update;
DROP TABLE IF EXISTS observe_logs;
DROP TABLE IF EXISTS observe_metrics;
