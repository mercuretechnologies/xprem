-- name: GetObserveErrorGroupSweepState :one
SELECT state FROM observe_error_group_sweep_state WHERE id = TRUE;

-- name: SaveObserveErrorGroupSweepState :exec
UPDATE observe_error_group_sweep_state SET state = @state::jsonb WHERE id = TRUE;
