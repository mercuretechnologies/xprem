package services

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"xprem/config"
	"xprem/internal/jobs"
	"xprem/internal/providers/expo"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// ErrHistoryImportAlreadyRunning refuses a second concurrent history import
// for the same app.
var ErrHistoryImportAlreadyRunning = errors.New("an update history import is already running for this app")

// ErrHistoryJobNotFound reports an unknown history import job.
var ErrHistoryJobNotFound = errors.New("this history import job does not exist")

// MaxHistoryImportGroups caps how many of the newest EAS update groups one
// history import may copy.
const MaxHistoryImportGroups = 50

const expoHistoryJobKind = "expo-history-import"

// expoHistoryArgs is the history import job: the update groups are snapshotted
// at enqueue time so a retry never needs the Expo token, which is not stored.
type expoHistoryArgs struct {
	AppId  string                 `json:"appId" river:"unique"`
	Total  int                    `json:"total"`
	Groups [][]expo.HistoryUpdate `json:"groups"`
}

func (expoHistoryArgs) Kind() string { return expoHistoryJobKind }

func (expoHistoryArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 5,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			// The default state list also blocks a new job while a finished
			// one is retained; this list only blocks while one is live.
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateRetryable,
				rivertype.JobStateScheduled,
			},
		},
	}
}

type expoHistoryWorker struct {
	river.WorkerDefaults[expoHistoryArgs]
	service *ExpoImportService
}

func (w *expoHistoryWorker) Timeout(*river.Job[expoHistoryArgs]) time.Duration {
	return time.Hour
}

func (w *expoHistoryWorker) Work(ctx context.Context, job *river.Job[expoHistoryArgs]) error {
	return w.service.copyHistory(ctx, job.Args.AppId, jobs.NewTracker(job.ID), job.Args.Groups)
}

// RegisterExpoImportWorker makes this replica able to work history imports.
func RegisterExpoImportWorker(workers *river.Workers, service *ExpoImportService) {
	river.AddWorker(workers, &expoHistoryWorker{service: service})
}

// ExpoHistoryJobStatus is the polled state of one background history import.
type ExpoHistoryJobStatus struct {
	State           string   `json:"state"`
	Total           int      `json:"total"`
	Processed       int      `json:"processed"`
	Imported        int      `json:"imported"`
	Skipped         []string `json:"skipped,omitempty"`
	Error           string   `json:"error,omitempty"`
	CancelRequested bool     `json:"cancelRequested"`
}

func historyJobStatus(job *rivertype.JobRow) *ExpoHistoryJobStatus {
	var args expoHistoryArgs
	_ = json.Unmarshal(job.EncodedArgs, &args)
	output := jobs.OutputOf(job)
	return &ExpoHistoryJobStatus{
		State:           jobs.UIState(job),
		Total:           args.Total,
		Processed:       output.Processed,
		Imported:        output.Succeeded,
		Skipped:         output.Warnings,
		Error:           jobs.LastError(job),
		CancelRequested: jobs.CancelRequested(job),
	}
}

// StartHistoryImport fetches the newest EAS update groups of an already
// imported app and copies them in a background job. It returns the job id the
// dashboard polls with GetHistoryJobStatus.
func (s *ExpoImportService) StartHistoryImport(ctx context.Context, auth types.Auth, expoAppId string, limit int) (string, error) {
	if !config.IsDBMode() {
		return "", store.ErrNotSupportedInStatelessMode
	}
	if err := requireExpoAuth(auth); err != nil {
		return "", err
	}
	parsedId, err := uuid.Parse(strings.TrimSpace(expoAppId))
	if err != nil {
		return "", validation.Errorf("expoAppId", "must be the Expo project UUID (app.json, extra.eas.projectId)")
	}
	if limit < 1 || limit > MaxHistoryImportGroups {
		return "", validation.Errorf("limit", "must be between 1 and %d", MaxHistoryImportGroups)
	}
	appId := parsedId.String()
	if _, err := s.apps.GetAppByID(ctx, appId); err != nil {
		return "", validation.Errorf("expoAppId", "this app is not imported yet, import it first")
	}

	groups, err := expo.FetchUpdateGroups(ctx, auth, appId, limit)
	if err != nil {
		return "", err
	}
	total := 0
	for _, group := range groups {
		total += len(group)
	}

	jobId, err := s.jobs.Enqueue(ctx, expoHistoryArgs{AppId: appId, Total: total, Groups: groups})
	if errors.Is(err, jobs.ErrAlreadyRunning) {
		return "", ErrHistoryImportAlreadyRunning
	}
	return jobId, err
}

// GetAppHistoryJob returns the id and status of the app's most recent history
// import job; false when none is known.
func (s *ExpoImportService) GetAppHistoryJob(ctx context.Context, appId string) (string, *ExpoHistoryJobStatus, bool) {
	parsed, err := uuid.Parse(strings.TrimSpace(appId))
	if err != nil {
		return "", nil, false
	}
	job, err := s.jobs.LatestByArg(ctx, expoHistoryJobKind, "appId", parsed.String())
	if err != nil || job == nil {
		return "", nil, false
	}
	return strconv.FormatInt(job.ID, 10), historyJobStatus(job), true
}

// CancelHistoryJob asks a running job to stop once the update it is copying
// is finished; canceling an already finished job is a no-op.
func (s *ExpoImportService) CancelHistoryJob(ctx context.Context, jobId string) error {
	found, err := s.jobs.Cancel(ctx, jobId)
	if err != nil {
		return err
	}
	if !found {
		return ErrHistoryJobNotFound
	}
	return nil
}

// GetHistoryJobStatus reads the state of one history import job; false when
// the job is unknown.
func (s *ExpoImportService) GetHistoryJobStatus(ctx context.Context, jobId string) (*ExpoHistoryJobStatus, bool) {
	job, err := s.jobs.Get(ctx, jobId)
	if err != nil || job == nil {
		return nil, false
	}
	return historyJobStatus(job), true
}
