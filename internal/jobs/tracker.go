package jobs

import (
	"context"
	"encoding/json"
	"log"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Output is a job's progress report, stored as the job's recorded output so
// the dashboard can poll it mid-run.
type Output struct {
	Processed int      `json:"processed"`
	Succeeded int      `json:"succeeded"`
	Warnings  []string `json:"warnings,omitempty"`
}

// OutputOf decodes a job's recorded progress; the zero Output when none.
func OutputOf(job *rivertype.JobRow) Output {
	var out Output
	if raw := job.Output(); raw != nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// Tracker counts a job's work items and persists each move as the job's
// output. Use it inside a worker's Work method.
type Tracker struct {
	jobId int64
	mu    sync.Mutex
	out   Output
}

func NewTracker(jobId int64) *Tracker {
	return &Tracker{jobId: jobId}
}

// Succeed records one successfully processed work item.
func (t *Tracker) Succeed(ctx context.Context) {
	t.mu.Lock()
	t.out.Processed++
	t.out.Succeeded++
	current := t.out
	t.mu.Unlock()
	t.persist(ctx, current)
}

// Skip records one work item left out, with the reason.
func (t *Tracker) Skip(ctx context.Context, reason string) {
	t.mu.Lock()
	t.out.Processed++
	t.out.Warnings = append(t.out.Warnings, reason)
	current := t.out
	t.mu.Unlock()
	t.persist(ctx, current)
}

// Output is the progress counted so far.
func (t *Tracker) Output() Output {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.out
}

func (t *Tracker) persist(ctx context.Context, current Output) {
	// No client in the context means the body runs outside River, as in
	// unit tests; anything after this check is a real failure worth logging.
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return
	}
	if err := river.RecordOutput(ctx, current); err != nil {
		log.Printf("[jobs] job %d could not record progress: %v", t.jobId, err)
		return
	}
	if _, err := client.JobUpdate(ctx, t.jobId, &river.JobUpdateParams{}); err != nil {
		log.Printf("[jobs] job %d could not persist progress: %v", t.jobId, err)
	}
}
