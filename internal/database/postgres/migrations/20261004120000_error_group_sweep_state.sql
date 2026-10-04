-- +goose Up

-- Mutable progress belongs in the control plane, so a new River worker or
-- replica resumes the sweep instead of starting at the same failing prefix.
-- Only a cursor and short-lived retry deadlines are stored; no telemetry or
-- existing error groups are rewritten.
CREATE TABLE observe_error_group_sweep_state (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    state JSONB NOT NULL DEFAULT '{}'::jsonb
);
INSERT INTO observe_error_group_sweep_state (id) VALUES (TRUE);

-- +goose Down
DROP TABLE IF EXISTS observe_error_group_sweep_state;
