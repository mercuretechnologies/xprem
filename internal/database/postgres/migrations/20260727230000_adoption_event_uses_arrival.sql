-- +goose Up

-- Date an adoption when the DEVICE moved, not when the server wrote the row.
--
-- The trigger stamped occurred_at with last_seen_at, the write instant, because
-- until now nothing else was available. A device that took an update while
-- offline and flushed its backlog two days later therefore reached ClickHouse
-- as having switched today, while PostgreSQL recorded the move two days ago:
-- the projected chart and the state fallback, which render into the same slot
-- of the same sheet, dated one adoption two days apart.
--
-- current_update_arrived_at is written in the same statement that changes
-- current_update_id, which is exactly what this trigger fires on, so it is
-- always set when we get here. The COALESCE is a floor, not a case anyone
-- expects to hit.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enqueue_device_update_state_event() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' AND NEW.current_update_id IS NOT NULL THEN
        INSERT INTO device_health_outbox (
            event_type, app_id, eas_client_id, update_id, occurred_at
        ) VALUES (
            'first_seen', NEW.app_id, NEW.eas_client_id, NEW.current_update_id,
            COALESCE(NEW.current_update_arrived_at, NEW.last_seen_at)
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
            COALESCE(NEW.current_update_arrived_at, NEW.last_seen_at)
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enqueue_device_update_state_event() RETURNS trigger AS $$
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
