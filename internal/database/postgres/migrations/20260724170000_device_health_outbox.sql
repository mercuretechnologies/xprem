-- +goose Up

-- Durable bridge from the PostgreSQL device source of truth to ClickHouse.
-- The triggers enqueue inside the same transaction as the state/failure
-- mutation; a background worker deletes a row only after ClickHouse accepts
-- it, so a ClickHouse outage never loses history or breaks manifest polling.
CREATE TABLE device_health_outbox (
    id BIGSERIAL PRIMARY KEY,
    event_type TEXT NOT NULL CHECK (event_type IN ('first_seen', 'switched', 'failure')),
    app_id UUID NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    eas_client_id UUID NOT NULL,
    update_id UUID NOT NULL,
    previous_update_id UUID,
    failure_type TEXT CHECK (failure_type IS NULL OR failure_type IN ('update_issue', 'runtime_issue')),
    fatal_error TEXT NOT NULL DEFAULT '',
    occurred_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose StatementBegin
CREATE FUNCTION enqueue_device_update_state_event() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' AND NEW.current_update_id IS NOT NULL THEN
        INSERT INTO device_health_outbox (
            event_type, app_id, eas_client_id, update_id, occurred_at
        ) VALUES (
            'first_seen', NEW.app_id, NEW.eas_client_id, NEW.current_update_id, NEW.last_seen_at
        );
    ELSIF TG_OP = 'UPDATE'
       AND NEW.current_update_id IS NOT NULL
       AND NEW.current_update_id IS DISTINCT FROM OLD.current_update_id THEN
        INSERT INTO device_health_outbox (
            event_type, app_id, eas_client_id, update_id, previous_update_id, occurred_at
        ) VALUES (
            CASE WHEN OLD.current_update_id IS NULL THEN 'first_seen' ELSE 'switched' END,
            NEW.app_id,
            NEW.eas_client_id,
            NEW.current_update_id,
            OLD.current_update_id,
            NEW.last_seen_at
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_device_update_state_event
AFTER INSERT OR UPDATE OF current_update_id ON device_identity
FOR EACH ROW EXECUTE FUNCTION enqueue_device_update_state_event();

-- device_update_failures is unique per (app, device, update, failure_type), so
-- AFTER INSERT emits one event per failure KIND while sticky manifest re-sends
-- and JS retries merely update last_seen_at. A device that reports both kinds
-- for one update therefore enqueues two events; they carry the same
-- (app, device, update) and update_crashes is keyed on exactly that triple with
-- no failure_type, so they collapse to one row on the ClickHouse side and the
-- affected-device count stays a device count.
-- +goose StatementBegin
CREATE FUNCTION enqueue_device_update_failure_event() RETURNS trigger AS $$
BEGIN
    INSERT INTO device_health_outbox (
        event_type, app_id, eas_client_id, update_id,
        failure_type, fatal_error, occurred_at
    ) VALUES (
        'failure', NEW.app_id, NEW.eas_client_id, NEW.update_id,
        NEW.failure_type, NEW.fatal_error, NEW.first_seen_at
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_device_update_failure_event
AFTER INSERT ON device_update_failures
FOR EACH ROW EXECUTE FUNCTION enqueue_device_update_failure_event();

-- +goose Down
DROP TRIGGER IF EXISTS trg_device_update_failure_event ON device_update_failures;
DROP FUNCTION IF EXISTS enqueue_device_update_failure_event;
DROP TRIGGER IF EXISTS trg_device_update_state_event ON device_identity;
DROP FUNCTION IF EXISTS enqueue_device_update_state_event;
DROP TABLE IF EXISTS device_health_outbox;
