-- +goose Up

-- Runtime failures describe the current JS session, unlike update_issue rows
-- which remain durable evidence of a native launch rollback. Keeping the row
-- and toggling resolved_at preserves the first error while allowing later
-- app_started/crash transitions to update instant-T health.
ALTER TABLE device_update_failures
    ADD COLUMN resolved_at TIMESTAMP WITH TIME ZONE;

-- Watermarks make source event time authoritative even when separate offline
-- batches arrive concurrently or in reverse order.
CREATE TABLE device_update_runtime_state (
    app_id UUID NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    eas_client_id UUID NOT NULL,
    update_id UUID NOT NULL,
    last_started_at TIMESTAMP WITH TIME ZONE,
    last_crashed_at TIMESTAMP WITH TIME ZONE,
    PRIMARY KEY (app_id, eas_client_id, update_id)
);

ALTER TABLE device_health_outbox
    DROP CONSTRAINT device_health_outbox_event_type_check;
ALTER TABLE device_health_outbox
    ADD CONSTRAINT device_health_outbox_event_type_check
    CHECK (event_type IN ('first_seen', 'switched', 'failure', 'recovered'));

-- +goose StatementBegin
CREATE FUNCTION enqueue_device_runtime_health_transition() RETURNS trigger AS $$
BEGIN
    IF OLD.resolved_at IS NULL AND NEW.resolved_at IS NOT NULL THEN
        INSERT INTO device_health_outbox (
            event_type, app_id, eas_client_id, update_id,
            failure_type, fatal_error, occurred_at
        ) VALUES (
            'recovered', NEW.app_id, NEW.eas_client_id, NEW.update_id,
            NEW.failure_type, NEW.fatal_error, NEW.resolved_at
        );
    ELSIF OLD.resolved_at IS NOT NULL AND NEW.resolved_at IS NULL THEN
        INSERT INTO device_health_outbox (
            event_type, app_id, eas_client_id, update_id,
            failure_type, fatal_error, occurred_at
        ) VALUES (
            'failure', NEW.app_id, NEW.eas_client_id, NEW.update_id,
            NEW.failure_type, NEW.fatal_error, NEW.last_seen_at
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_device_runtime_health_transition
AFTER UPDATE OF resolved_at ON device_update_failures
FOR EACH ROW EXECUTE FUNCTION enqueue_device_runtime_health_transition();

-- +goose Down

DROP TRIGGER trg_device_runtime_health_transition ON device_update_failures;
DROP FUNCTION enqueue_device_runtime_health_transition;

-- Old binaries cannot consume this event type. Removing pending recovery
-- events is required before restoring their narrower check constraint.
DELETE FROM device_health_outbox WHERE event_type = 'recovered';
ALTER TABLE device_health_outbox
    DROP CONSTRAINT device_health_outbox_event_type_check;
ALTER TABLE device_health_outbox
    ADD CONSTRAINT device_health_outbox_event_type_check
    CHECK (event_type IN ('first_seen', 'switched', 'failure'));

DROP TABLE device_update_runtime_state;
ALTER TABLE device_update_failures
    DROP COLUMN resolved_at;
