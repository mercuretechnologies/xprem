package jobs

import (
	"encoding/json"

	"github.com/riverqueue/river/rivertype"
)

// The four states a dashboard cares about; River's eight collapse into these.
const (
	StateRunning  = "running"
	StateDone     = "done"
	StateFailed   = "failed"
	StateCanceled = "canceled"
)

// UIState maps a job's River state to the dashboard vocabulary. A job waiting
// for a retry counts as running: it will run again without anyone's help.
func UIState(job *rivertype.JobRow) string {
	switch job.State {
	case rivertype.JobStateCompleted:
		return StateDone
	case rivertype.JobStateCancelled:
		return StateCanceled
	case rivertype.JobStateDiscarded:
		return StateFailed
	default:
		return StateRunning
	}
}

// LastError is the job's final error message; empty until the job is
// discarded, since earlier errors only mean a retry is coming.
func LastError(job *rivertype.JobRow) string {
	if job.State != rivertype.JobStateDiscarded || len(job.Errors) == 0 {
		return ""
	}
	return job.Errors[len(job.Errors)-1].Error
}

// CancelRequested reports whether someone asked a still-running job to stop.
func CancelRequested(job *rivertype.JobRow) bool {
	if job.State != rivertype.JobStateRunning {
		return false
	}
	var metadata struct {
		CancelAttemptedAt *string `json:"cancel_attempted_at"`
	}
	if err := json.Unmarshal(job.Metadata, &metadata); err != nil {
		return false
	}
	return metadata.CancelAttemptedAt != nil
}
