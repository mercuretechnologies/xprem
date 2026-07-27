-- +goose Up

-- The whole observe schema in one file. It replaces eleven migrations that
-- were the branch's own history rather than anyone's upgrade path: tables
-- created then dropped and recreated three commits later, four separate ALTERs
-- adding one column each, and a dedup column added then removed. observe has
-- never shipped, so none of that was an upgrade anybody had to make. Replaying
-- it only made a fresh install build a schema in order to unbuild parts of it,
-- and reading the schema meant reading eleven files and applying them in order
-- in one's head.
--
-- Idempotent rather than a squash under an already-applied version number.
-- goose runs here with WithAllowMissing, so it applies any migration it has
-- not recorded whatever its number: a database halfway through the old
-- sequence would silently never receive the rest if this reused the first
-- version. CREATE ... IF NOT EXISTS plus one ADD COLUMN IF NOT EXISTS per
-- column instead, so a fresh database, an up-to-date one and a halfway one all
-- converge on the same schema.

CREATE TABLE IF NOT EXISTS observe_metrics
(
    `app_id` UUID,
    `eas_client_id` UUID,
    `update_id` UUID,
    `update_group_id` UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000'),
    `branch` LowCardinality(String),
    `channel` LowCardinality(String),
    `runtime_version` LowCardinality(String),
    `platform` LowCardinality(String),
    `session_id` UUID,
    `metric_name` LowCardinality(String),
    `value` Float64,
    `route_name` String DEFAULT '',
    `custom_params` String DEFAULT '',
    `attributes` String DEFAULT '',
    `os_name` LowCardinality(String),
    `os_version` LowCardinality(String),
    `device_model` LowCardinality(String) DEFAULT '',
    `country_code` LowCardinality(String) DEFAULT '',
    `lat` Nullable(Float64),
    `lng` Nullable(Float64),
    `app_version` LowCardinality(String),
    `app_build_number` LowCardinality(String) DEFAULT '',
    `eas_build_id` LowCardinality(String) DEFAULT '',
    `environment` LowCardinality(String) DEFAULT '',
    `sdk_version` LowCardinality(String),
    `timestamp` DateTime64(9, 'UTC'),
    `ingested_at` DateTime('UTC') DEFAULT now(),
    `content_key` UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000'),
    INDEX idx_metrics_client eas_client_id TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(timestamp)
ORDER BY (app_id, update_id, timestamp)
SETTINGS index_granularity = 8192;

CREATE TABLE IF NOT EXISTS observe_logs
(
    `app_id` UUID,
    `eas_client_id` UUID,
    `update_id` UUID,
    `update_group_id` UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000'),
    `branch` LowCardinality(String),
    `channel` LowCardinality(String),
    `runtime_version` LowCardinality(String),
    `platform` LowCardinality(String),
    `session_id` UUID,
    `event_name` LowCardinality(String),
    `severity_number` UInt8,
    `severity_text` LowCardinality(String),
    `is_fatal` UInt8 DEFAULT 0,
    `body` String DEFAULT '',
    `attributes` String DEFAULT '',
    `os_name` LowCardinality(String),
    `os_version` LowCardinality(String),
    `device_model` LowCardinality(String) DEFAULT '',
    `country_code` LowCardinality(String) DEFAULT '',
    `lat` Nullable(Float64),
    `lng` Nullable(Float64),
    `app_version` LowCardinality(String),
    `app_build_number` LowCardinality(String) DEFAULT '',
    `eas_build_id` LowCardinality(String) DEFAULT '',
    `environment` LowCardinality(String) DEFAULT '',
    `sdk_version` LowCardinality(String),
    `timestamp` DateTime64(9, 'UTC'),
    `ingested_at` DateTime('UTC') DEFAULT now(),
    `content_key` UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000'),
    INDEX idx_logs_client eas_client_id TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(timestamp)
ORDER BY (app_id, update_id, timestamp)
SETTINGS index_granularity = 8192;

CREATE TABLE IF NOT EXISTS device_health_events
(
    `outbox_id` UInt64,
    `event_type` LowCardinality(String),
    `app_id` UUID,
    `eas_client_id` UUID,
    `update_id` UUID,
    `branch` LowCardinality(String) DEFAULT '',
    `runtime_version` LowCardinality(String) DEFAULT '',
    `platform` LowCardinality(String) DEFAULT '',
    `os_name` LowCardinality(String) DEFAULT '',
    `os_version` LowCardinality(String) DEFAULT '',
    `device_model` LowCardinality(String) DEFAULT '',
    `country_code` LowCardinality(String) DEFAULT '',
    `app_version` LowCardinality(String) DEFAULT '',
    `previous_update_id` Nullable(UUID),
    `failure_type` LowCardinality(String) DEFAULT '',
    `fatal_error` String DEFAULT '',
    `occurred_at` DateTime64(3, 'UTC'),
    `ingested_at` DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMM(occurred_at)
ORDER BY (app_id, outbox_id)
SETTINGS index_granularity = 8192;

CREATE TABLE IF NOT EXISTS update_health_snapshots
(
    `app_id` UUID,
    `update_id` UUID,
    `bucket` DateTime('UTC'),
    `captured_at` DateTime64(3, 'UTC'),
    `role` LowCardinality(String),
    `devices_on_update` UInt64,
    `successful_devices` UInt64,
    `faulty_devices` UInt64,
    `update_issues` UInt64,
    `runtime_issues` UInt64
)
ENGINE = ReplacingMergeTree(captured_at)
PARTITION BY toYYYYMM(bucket)
ORDER BY (app_id, update_id, bucket)
SETTINGS index_granularity = 8192;

-- Every column again, so a table that already exists in an older shape is
-- brought up rather than left behind by the CREATEs above.

ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `outbox_id` UInt64;
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `event_type` LowCardinality(String);
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `app_id` UUID;
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `eas_client_id` UUID;
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `update_id` UUID;
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `branch` LowCardinality(String) DEFAULT '';
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `runtime_version` LowCardinality(String) DEFAULT '';
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `platform` LowCardinality(String) DEFAULT '';
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `os_name` LowCardinality(String) DEFAULT '';
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `os_version` LowCardinality(String) DEFAULT '';
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `device_model` LowCardinality(String) DEFAULT '';
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `country_code` LowCardinality(String) DEFAULT '';
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `app_version` LowCardinality(String) DEFAULT '';
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `previous_update_id` Nullable(UUID);
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `failure_type` LowCardinality(String) DEFAULT '';
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `fatal_error` String DEFAULT '';
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `occurred_at` DateTime64(3, 'UTC');
ALTER TABLE device_health_events ADD COLUMN IF NOT EXISTS `ingested_at` DateTime64(3, 'UTC') DEFAULT now64(3);

ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `app_id` UUID;
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `eas_client_id` UUID;
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `update_id` UUID;
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `update_group_id` UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000');
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `branch` LowCardinality(String);
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `channel` LowCardinality(String);
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `runtime_version` LowCardinality(String);
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `platform` LowCardinality(String);
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `session_id` UUID;
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `event_name` LowCardinality(String);
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `severity_number` UInt8;
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `severity_text` LowCardinality(String);
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `is_fatal` UInt8 DEFAULT 0;
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `body` String DEFAULT '';
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `attributes` String DEFAULT '';
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `os_name` LowCardinality(String);
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `os_version` LowCardinality(String);
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `device_model` LowCardinality(String) DEFAULT '';
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `country_code` LowCardinality(String) DEFAULT '';
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `lat` Nullable(Float64);
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `lng` Nullable(Float64);
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `app_version` LowCardinality(String);
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `app_build_number` LowCardinality(String) DEFAULT '';
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `eas_build_id` LowCardinality(String) DEFAULT '';
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `environment` LowCardinality(String) DEFAULT '';
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `sdk_version` LowCardinality(String);
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `timestamp` DateTime64(9, 'UTC');
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `ingested_at` DateTime('UTC') DEFAULT now();
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS `content_key` UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000');

ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `app_id` UUID;
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `eas_client_id` UUID;
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `update_id` UUID;
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `update_group_id` UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000');
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `branch` LowCardinality(String);
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `channel` LowCardinality(String);
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `runtime_version` LowCardinality(String);
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `platform` LowCardinality(String);
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `session_id` UUID;
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `metric_name` LowCardinality(String);
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `value` Float64;
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `route_name` String DEFAULT '';
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `custom_params` String DEFAULT '';
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `attributes` String DEFAULT '';
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `os_name` LowCardinality(String);
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `os_version` LowCardinality(String);
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `device_model` LowCardinality(String) DEFAULT '';
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `country_code` LowCardinality(String) DEFAULT '';
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `lat` Nullable(Float64);
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `lng` Nullable(Float64);
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `app_version` LowCardinality(String);
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `app_build_number` LowCardinality(String) DEFAULT '';
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `eas_build_id` LowCardinality(String) DEFAULT '';
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `environment` LowCardinality(String) DEFAULT '';
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `sdk_version` LowCardinality(String);
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `timestamp` DateTime64(9, 'UTC');
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `ingested_at` DateTime('UTC') DEFAULT now();
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS `content_key` UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000');

ALTER TABLE update_health_snapshots ADD COLUMN IF NOT EXISTS `app_id` UUID;
ALTER TABLE update_health_snapshots ADD COLUMN IF NOT EXISTS `update_id` UUID;
ALTER TABLE update_health_snapshots ADD COLUMN IF NOT EXISTS `bucket` DateTime('UTC');
ALTER TABLE update_health_snapshots ADD COLUMN IF NOT EXISTS `captured_at` DateTime64(3, 'UTC');
ALTER TABLE update_health_snapshots ADD COLUMN IF NOT EXISTS `role` LowCardinality(String);
ALTER TABLE update_health_snapshots ADD COLUMN IF NOT EXISTS `devices_on_update` UInt64;
ALTER TABLE update_health_snapshots ADD COLUMN IF NOT EXISTS `successful_devices` UInt64;
ALTER TABLE update_health_snapshots ADD COLUMN IF NOT EXISTS `faulty_devices` UInt64;
ALTER TABLE update_health_snapshots ADD COLUMN IF NOT EXISTS `update_issues` UInt64;
ALTER TABLE update_health_snapshots ADD COLUMN IF NOT EXISTS `runtime_issues` UInt64;

-- content_hash was the 64-bit dedup fingerprint that content_key replaced. It
-- only ever existed on this branch.
ALTER TABLE observe_logs DROP COLUMN IF EXISTS content_hash;
ALTER TABLE observe_metrics DROP COLUMN IF EXISTS content_hash;

-- +goose Down
DROP TABLE IF EXISTS update_health_snapshots;
DROP TABLE IF EXISTS device_health_events;
DROP TABLE IF EXISTS observe_logs;
DROP TABLE IF EXISTS observe_metrics;
