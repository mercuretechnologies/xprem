package services

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"time"
	"xprem/config"
	"xprem/internal/auditlog"
	"xprem/internal/bsdiff"
	"xprem/internal/bucket"
	"xprem/internal/jobs"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// BundlePatchRepository is the durable record of every patch job, the one the
// dashboard and the MCP read; River's own rows are purged within days.
type BundlePatchRepository interface {
	MarkPending(ctx context.Context, appId, branch, targetUpdateId, sourceUpdateId string) error
	MarkRunning(ctx context.Context, appId, branch, targetUpdateId, sourceUpdateId string) error
	Finish(ctx context.Context, appId, branch, targetUpdateId, sourceUpdateId string, status types.BundlePatchStatus, reason string, patchSize, fullDownloadSize *int64) error
	ListByTarget(ctx context.Context, appId, branch, targetUpdateId string) ([]types.BundlePatch, error)
}

var (
	ErrBundleDiffingUnavailable = errors.New("bundle diffing is disabled or needs the control plane")
	ErrUpdateHasNoBundle        = errors.New("this update is a rollback and has no bundle to patch")
)

type BsDiffService struct {
	bucket        bucket.Bucket
	jobs          *jobs.Client
	updateService *UpdateService
	updateRepo    UpdateRepository
	patches       BundlePatchRepository
	onAuditEvent  auditlog.RecordFunc
}

func NewBSDiffService(bucket bucket.Bucket, jobsClient *jobs.Client, updateService *UpdateService, updateRepo UpdateRepository, patches BundlePatchRepository) *BsDiffService {
	return &BsDiffService{
		bucket:        bucket,
		jobs:          jobsClient,
		updateService: updateService,
		updateRepo:    updateRepo,
		patches:       patches,
	}
}

func (s *BsDiffService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

func (s *BsDiffService) available() bool {
	return config.IsBundleDiffingEnabled() && config.IsDBMode() && s.patches != nil
}

// ListPatches is the bundle_patches record of one target update.
func (s *BsDiffService) ListPatches(ctx context.Context, appId, branch, updateId string) ([]types.BundlePatch, error) {
	if !s.available() {
		return nil, ErrBundleDiffingUnavailable
	}
	if err := validation.Name("branchName", branch); err != nil {
		return nil, err
	}
	if err := validation.Name("updateId", updateId); err != nil {
		return nil, err
	}
	patches, err := s.patches.ListByTarget(ctx, appId, branch, updateId)
	if err != nil {
		return nil, err
	}
	if patches == nil {
		patches = []types.BundlePatch{}
	}
	return patches, nil
}

// RecomputePatches plans the patches toward an update again, exactly as its
// publish did, and returns how many it planned.
func (s *BsDiffService) RecomputePatches(ctx context.Context, appId, branch, runtimeVersion, updateId string) (int, error) {
	if !s.available() {
		return 0, ErrBundleDiffingUnavailable
	}
	details, err := s.updateService.GetUpdateDetails(ctx, appId, branch, runtimeVersion, updateId)
	if err != nil {
		return 0, err
	}
	if details.Type != types.NormalUpdate {
		return 0, ErrUpdateHasNoBundle
	}
	update := types.Update{AppId: appId, Branch: branch, RuntimeVersion: runtimeVersion, UpdateId: updateId}
	planned, err := s.ComputeBSDiffForPreviousUpdates(ctx, &update, details.UpdateUUID, details.Platform)
	if err != nil {
		return 0, err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionUpdatePatchesRecomputed,
		TargetType:    "update",
		TargetID:      updateId,
		TargetDisplay: updateId,
		AppID:         appId,
		Metadata:      map[string]any{"branch": branch, "runtime_version": runtimeVersion, "scheduled": planned},
	})
	return planned, nil
}

const bsDiffComputeJobKind = "bsdiff-compute"

// bsDiffComputeArgs names the pair by UUID for uniqueness and by row for the
// bundle_patches record, which stays reachable even when a lookup fails.
type bsDiffComputeArgs struct {
	AppId            string `json:"appId" river:"unique"`
	TargetUpdateUUID string `json:"targetUpdateUUID" river:"unique"`
	SourceUpdateUUID string `json:"sourceUpdateUUID" river:"unique"`
	Branch           string `json:"branch"`
	TargetUpdateId   string `json:"targetUpdateId"`
	SourceUpdateId   string `json:"sourceUpdateId"`
}

func (bsDiffComputeArgs) Kind() string { return bsDiffComputeJobKind }

func (bsDiffComputeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       jobs.QueueBSDiff,
		MaxAttempts: 5,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			// Unique only while the job is alive: a finished pair can be recomputed.
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateScheduled,
				rivertype.JobStateRunning,
				rivertype.JobStateRetryable,
			},
		},
	}
}

type bsDiffComputeWorker struct {
	river.WorkerDefaults[bsDiffComputeArgs]
	service *BsDiffService
}

func (w *bsDiffComputeWorker) Timeout(*river.Job[bsDiffComputeArgs]) time.Duration {
	return time.Minute * 15
}

func (w *bsDiffComputeWorker) Work(ctx context.Context, job *river.Job[bsDiffComputeArgs]) error {
	return w.service.runPatchJob(ctx, job)
}

func RegisterBSDiffWorker(workers *river.Workers, service *BsDiffService) {
	river.AddWorker(workers, &bsDiffComputeWorker{service: service})
}

// maxPatchSources is how many earlier updates of the same platform get a patch toward a freshly published one.
const maxPatchSources = 5

// ComputeBSDiffForPreviousUpdates plans one patch job per earlier update of
// the same platform, newest first, and returns how many it planned.
func (s *BsDiffService) ComputeBSDiffForPreviousUpdates(ctx context.Context, update *types.Update, updateUUID string, platform types.Platform) (int, error) {
	if !config.IsBundleDiffingEnabled() {
		return 0, nil
	}
	if _, err := uuid.Parse(updateUUID); err != nil {
		return 0, fmt.Errorf("update %s has no usable UUID: %w", update.UpdateId, err)
	}
	// The listing pages by id descending, so starting past the target's id
	// yields only the updates published before it.
	targetId, err := strconv.ParseInt(update.UpdateId, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("update id %q is not numeric: %w", update.UpdateId, err)
	}
	cursor := &targetId
	enqueued := 0
	for page := 0; page < 4 && enqueued < maxPatchSources; page++ {
		updatesPage, err := s.updateRepo.GetUpdatesByRunTimeVersionAndBranchName(ctx, update.AppId, update.RuntimeVersion, update.Branch, cursor, 2*maxPatchSources)
		if err != nil {
			return enqueued, err
		}
		for _, item := range updatesPage.Items {
			if enqueued == maxPatchSources {
				break
			}
			// Rollbacks carry a label instead of a UUID and have no bundle.
			if _, err := uuid.Parse(item.UpdateUUID); err != nil {
				continue
			}
			if item.Platform != platform {
				continue
			}
			if err := s.enqueuePatch(ctx, bsDiffComputeArgs{
				AppId:            update.AppId,
				TargetUpdateUUID: updateUUID,
				SourceUpdateUUID: item.UpdateUUID,
				Branch:           update.Branch,
				TargetUpdateId:   update.UpdateId,
				SourceUpdateId:   item.UpdateId,
			}); err != nil {
				return enqueued, err
			}
			enqueued++
		}
		if updatesPage.NextCursor == nil {
			break
		}
		last, err := strconv.ParseInt(*updatesPage.NextCursor, 10, 64)
		if err != nil {
			return enqueued, fmt.Errorf("invalid updates cursor %q: %w", *updatesPage.NextCursor, err)
		}
		cursor = &last
	}
	return enqueued, nil
}

// enqueuePatch records the pair as pending, then inserts the job: the worker
// can start the moment the job exists, and must find the row.
func (s *BsDiffService) enqueuePatch(ctx context.Context, args bsDiffComputeArgs) error {
	s.record(ctx, args, func(ctx context.Context) error {
		return s.patches.MarkPending(ctx, args.AppId, args.Branch, args.TargetUpdateId, args.SourceUpdateId)
	})
	_, err := s.jobs.Enqueue(ctx, args)
	if err != nil && !errors.Is(err, jobs.ErrAlreadyRunning) {
		return fmt.Errorf("enqueue patch %s -> %s: %w", args.SourceUpdateUUID, args.TargetUpdateUUID, err)
	}
	return nil
}

// patchOutcome is how a job ended without an error: a stored patch, or a
// documented reason not to store one.
type patchOutcome struct {
	status           types.BundlePatchStatus
	reason           string
	patchSize        *int64
	fullDownloadSize *int64
}

func skipped(reason string) patchOutcome {
	return patchOutcome{status: types.BundlePatchSkipped, reason: reason}
}

// runPatchJob wraps computeBSDiff with the bundle_patches bookkeeping.
func (s *BsDiffService) runPatchJob(ctx context.Context, job *river.Job[bsDiffComputeArgs]) error {
	args := job.Args
	s.record(ctx, args, func(ctx context.Context) error {
		return s.patches.MarkRunning(ctx, args.AppId, args.Branch, args.TargetUpdateId, args.SourceUpdateId)
	})

	outcome, err := s.computeBSDiff(ctx, args.AppId, args.TargetUpdateUUID, args.SourceUpdateUUID)
	if err == nil {
		s.record(ctx, args, func(ctx context.Context) error {
			return s.patches.Finish(ctx, args.AppId, args.Branch, args.TargetUpdateId, args.SourceUpdateId, outcome.status, outcome.reason, outcome.patchSize, outcome.fullDownloadSize)
		})
		return nil
	}

	var cancel *river.JobCancelError
	status := types.BundlePatchRunning
	switch {
	case errors.As(err, &cancel):
		status = types.BundlePatchCancelled
	case job.Attempt >= job.MaxAttempts:
		status = types.BundlePatchFailed
	}
	s.record(ctx, args, func(ctx context.Context) error {
		return s.patches.Finish(ctx, args.AppId, args.Branch, args.TargetUpdateId, args.SourceUpdateId, status, err.Error(), nil, nil)
	})
	return err
}

// record runs a bookkeeping write and logs its failure: the patch itself is
// what matters, the record must never fail the job.
func (s *BsDiffService) record(ctx context.Context, args bsDiffComputeArgs, write func(context.Context) error) {
	if s.patches == nil {
		return
	}
	if err := write(ctx); err != nil {
		log.Printf("[bsdiff] cannot record patch %s -> %s: %v", args.SourceUpdateUUID, args.TargetUpdateUUID, err)
	}
}

var (
	errBundleTooLarge = errors.New("bundle exceeds the patch size limit")
	errBlobMissing    = errors.New("bundle blob is missing from the bucket")
)

// readBundle loads the launch asset of an update from the CAS. Updates
// published before the CAS carry no mapping and get nil, nil: no patch.
func (s *BsDiffService) readBundle(ctx context.Context, appId string, mapping *types.UpdateAssetMapping) ([]byte, error) {
	if mapping == nil || mapping.LaunchAsset.Hash == "" {
		return nil, nil
	}
	blob, err := s.bucket.GetBlob(ctx, appId, mapping.LaunchAsset.Hash)
	if err != nil {
		return nil, fmt.Errorf("reading blob %s: %w", mapping.LaunchAsset.Hash, err)
	}
	if blob == nil {
		return nil, fmt.Errorf("%w: %s", errBlobMissing, mapping.LaunchAsset.Hash)
	}
	defer blob.Reader.Close()
	maxSize := config.BundleDiffingMaxBundleSize()
	data, err := io.ReadAll(io.LimitReader(blob.Reader, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading blob %s: %w", mapping.LaunchAsset.Hash, err)
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("%w: %s", errBundleTooLarge, mapping.LaunchAsset.Hash)
	}
	return data, nil
}

func (s *BsDiffService) loadUpdate(ctx context.Context, appId, updateUUID string) (*types.Update, error) {
	update, err := s.updateRepo.GetUpdateByUUID(ctx, appId, updateUUID)
	if err != nil {
		return nil, fmt.Errorf("retrieving update %s: %w", updateUUID, err)
	}
	if update == nil {
		return nil, river.JobCancel(fmt.Errorf("%s: update %s not found", types.BundlePatchReasonUpdateNotFound, updateUUID))
	}
	return update, nil
}

// gzippedSize is what a full download of data costs, which is what a patch
// must beat.
func gzippedSize(data []byte) (int, error) {
	var n countingWriter
	gz := gzip.NewWriter(&n)
	if _, err := gz.Write(data); err != nil {
		return 0, err
	}
	if err := gz.Close(); err != nil {
		return 0, err
	}
	return n.n, nil
}

type countingWriter struct{ n int }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

// computeBSDiff builds, verifies and stores the patch from the source
// update's bundle to the target's. Conditions that a retry cannot fix cancel
// the job; a missing bundle or a patch not worth serving is a skip.
func (s *BsDiffService) computeBSDiff(ctx context.Context, appId, targetUpdateUUID, sourceUpdateUUID string) (patchOutcome, error) {
	target, err := s.loadUpdate(ctx, appId, targetUpdateUUID)
	if err != nil {
		return patchOutcome{}, err
	}
	source, err := s.loadUpdate(ctx, appId, sourceUpdateUUID)
	if err != nil {
		return patchOutcome{}, err
	}
	if source.Branch != target.Branch {
		return patchOutcome{}, river.JobCancel(fmt.Errorf("%s: source %s and target %s are on different branches", types.BundlePatchReasonDifferentBranch, sourceUpdateUUID, targetUpdateUUID))
	}

	targetMapping, err := s.updateRepo.GetUpdateAssetMapping(ctx, *target)
	if err != nil {
		return patchOutcome{}, fmt.Errorf("retrieving asset mapping of %s: %w", targetUpdateUUID, err)
	}
	targetBundle, err := s.readBundle(ctx, appId, targetMapping)
	if err != nil {
		return patchOutcome{}, cancelIfPermanent(err)
	}
	if targetBundle == nil {
		return skipped(types.BundlePatchReasonLegacyUpdate), nil
	}
	sourceMapping, err := s.updateRepo.GetUpdateAssetMapping(ctx, *source)
	if err != nil {
		return patchOutcome{}, fmt.Errorf("retrieving asset mapping of %s: %w", sourceUpdateUUID, err)
	}
	sourceBundle, err := s.readBundle(ctx, appId, sourceMapping)
	if err != nil {
		return patchOutcome{}, cancelIfPermanent(err)
	}
	if sourceBundle == nil {
		return skipped(types.BundlePatchReasonLegacyUpdate), nil
	}
	if bytes.Equal(sourceBundle, targetBundle) {
		return skipped(types.BundlePatchReasonIdenticalBundles), nil
	}

	patch, err := bsdiff.Diff(sourceBundle, targetBundle)
	if err != nil {
		return patchOutcome{}, river.JobCancel(fmt.Errorf("computing patch %s -> %s: %w", sourceUpdateUUID, targetUpdateUUID, err))
	}
	rebuilt, err := bsdiff.Patch(sourceBundle, patch)
	if err != nil || !bytes.Equal(rebuilt, targetBundle) {
		return patchOutcome{}, river.JobCancel(fmt.Errorf("%s: patch %s -> %s does not rebuild the target bundle: %v", types.BundlePatchReasonVerificationFailed, sourceUpdateUUID, targetUpdateUUID, err))
	}

	fullDownload, err := gzippedSize(targetBundle)
	if err != nil {
		return patchOutcome{}, err
	}
	patchSize, fullSize := int64(len(patch)), int64(fullDownload)
	if float64(len(patch)) > config.BundleDiffingPatchMaxRatio()*float64(fullDownload) {
		log.Printf("[bsdiff] patch %s -> %s not worth serving: %d bytes against a %d byte download", sourceUpdateUUID, targetUpdateUUID, len(patch), fullDownload)
		return patchOutcome{status: types.BundlePatchSkipped, reason: types.BundlePatchReasonNotWorth, patchSize: &patchSize, fullDownloadSize: &fullSize}, nil
	}

	if err := s.bucket.PutBSDiff(ctx, appId, target.Branch, targetUpdateUUID, sourceUpdateUUID, bytes.NewReader(patch)); err != nil {
		return patchOutcome{}, fmt.Errorf("storing patch %s -> %s: %w", sourceUpdateUUID, targetUpdateUUID, err)
	}
	log.Printf("[bsdiff] stored patch %s -> %s: %d bytes against a %d byte download", sourceUpdateUUID, targetUpdateUUID, len(patch), fullDownload)
	return patchOutcome{status: types.BundlePatchStored, patchSize: &patchSize, fullDownloadSize: &fullSize}, nil
}

func cancelIfPermanent(err error) error {
	switch {
	case errors.Is(err, errBundleTooLarge):
		return river.JobCancel(fmt.Errorf("%s: %w", types.BundlePatchReasonBundleTooLarge, err))
	case errors.Is(err, errBlobMissing):
		return river.JobCancel(fmt.Errorf("%s: %w", types.BundlePatchReasonBlobMissing, err))
	}
	return err
}
