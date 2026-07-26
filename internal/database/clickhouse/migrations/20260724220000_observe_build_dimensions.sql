-- +goose Up

-- Resource dimensions emitted by expo-observe but previously discarded.
-- They power the environment/build filters and make the dashboard's build
-- count distinct from the app-version (release) count.
ALTER TABLE observe_metrics
    ADD COLUMN IF NOT EXISTS app_build_number LowCardinality(String) DEFAULT '' AFTER app_version;
ALTER TABLE observe_metrics
    ADD COLUMN IF NOT EXISTS eas_build_id LowCardinality(String) DEFAULT '' AFTER app_build_number;
ALTER TABLE observe_metrics
    ADD COLUMN IF NOT EXISTS environment LowCardinality(String) DEFAULT '' AFTER eas_build_id;

ALTER TABLE observe_logs
    ADD COLUMN IF NOT EXISTS app_build_number LowCardinality(String) DEFAULT '' AFTER app_version;
ALTER TABLE observe_logs
    ADD COLUMN IF NOT EXISTS eas_build_id LowCardinality(String) DEFAULT '' AFTER app_build_number;
ALTER TABLE observe_logs
    ADD COLUMN IF NOT EXISTS environment LowCardinality(String) DEFAULT '' AFTER eas_build_id;

-- +goose Down

ALTER TABLE observe_logs DROP COLUMN IF EXISTS environment;
ALTER TABLE observe_logs DROP COLUMN IF EXISTS eas_build_id;
ALTER TABLE observe_logs DROP COLUMN IF EXISTS app_build_number;

ALTER TABLE observe_metrics DROP COLUMN IF EXISTS environment;
ALTER TABLE observe_metrics DROP COLUMN IF EXISTS eas_build_id;
ALTER TABLE observe_metrics DROP COLUMN IF EXISTS app_build_number;
