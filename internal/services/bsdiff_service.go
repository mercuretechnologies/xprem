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
	"xprem/internal/bsdiff"
	"xprem/internal/bucket"
	"xprem/internal/jobs"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type BsDiffService struct {
	bucket        bucket.Bucket
	jobs          *jobs.Client
	updateService *UpdateService
	updateRepo    UpdateRepository
}

func NewBSDiffService(bucket bucket.Bucket, jobsClient *jobs.Client, updateService *UpdateService, updateRepo UpdateRepository) *BsDiffService {
	return &BsDiffService{
		bucket:        bucket,
		jobs:          jobsClient,
		updateService: updateService,
		updateRepo:    updateRepo,
	}
}

const bsDiffComputeJobKind = "bsdiff-compute"

type bsDiffComputeArgs struct {
	TargetUpdateUUID string `json:"targetUpdateUUID" river:"unique"`
	SourceUpdateUUID string `json:"sourceUpdateUUID" river:"unique"`
	AppId            string `json:"appId" river:"unique"`
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
	return w.service.computeBSDiff(ctx, job.Args.AppId, job.Args.TargetUpdateUUID, job.Args.SourceUpdateUUID)
}

func RegisterBSDiffWorker(workers *river.Workers, service *BsDiffService) {
	river.AddWorker(workers, &bsDiffComputeWorker{service: service})
}

// maxPatchSources is how many earlier updates of the same platform get a patch toward a freshly published one.
const maxPatchSources = 5

func (s *BsDiffService) ComputeBSDiffForPreviousUpdates(ctx context.Context, update *types.Update, updateUUID string, platform types.Platform) error {
	if !config.IsBundleDiffingEnabled() {
		return nil
	}
	if !config.IsDBMode() {
		log.Printf("[bsdiff] no patches for update %s: bundle diffing needs the control plane (database mode)", update.UpdateId)
		return nil
	}
	var cursor *int64
	enqueued := 0
	for page := 0; page < 4 && enqueued < maxPatchSources; page++ {
		updatesPage, err := s.updateRepo.GetUpdatesByRunTimeVersionAndBranchName(ctx, update.AppId, update.RuntimeVersion, update.Branch, cursor, 2*maxPatchSources)
		if err != nil {
			return err
		}
		for _, item := range updatesPage.Items {
			if enqueued == maxPatchSources {
				break
			}
			// Rollbacks carry a label instead of a UUID and have no bundle.
			if _, err := uuid.Parse(item.UpdateUUID); err != nil {
				continue
			}
			if item.Platform != platform || item.UpdateUUID == updateUUID {
				continue
			}
			_, err := s.jobs.Enqueue(ctx, bsDiffComputeArgs{
				TargetUpdateUUID: updateUUID,
				SourceUpdateUUID: item.UpdateUUID,
				AppId:            update.AppId,
			})
			if err != nil && !errors.Is(err, jobs.ErrAlreadyRunning) {
				return fmt.Errorf("enqueue patch %s -> %s: %w", item.UpdateUUID, updateUUID, err)
			}
			enqueued++
		}
		if updatesPage.NextCursor == nil {
			break
		}
		last, err := strconv.ParseInt(*updatesPage.NextCursor, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid updates cursor %q: %w", *updatesPage.NextCursor, err)
		}
		cursor = &last
	}
	return nil
}

var (
	errBundleTooLarge = errors.New("bundle exceeds the patch size limit")
	errBlobMissing    = errors.New("bundle blob is missing from the bucket")
)

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
		return nil, river.JobCancel(fmt.Errorf("update %s not found", updateUUID))
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

func (s *BsDiffService) computeBSDiff(ctx context.Context, appId, targetUpdateUUID, sourceUpdateUUID string) error {
	target, err := s.loadUpdate(ctx, appId, targetUpdateUUID)
	if err != nil {
		return err
	}
	source, err := s.loadUpdate(ctx, appId, sourceUpdateUUID)
	if err != nil {
		return err
	}
	if source.Branch != target.Branch {
		return river.JobCancel(fmt.Errorf("source %s and target %s are on different branches", sourceUpdateUUID, targetUpdateUUID))
	}
	if source.RuntimeVersion != target.RuntimeVersion {
		return river.JobCancel(fmt.Errorf("source %s and target %s are on different runtime versions", sourceUpdateUUID, targetUpdateUUID))
	}
	targetMapping, err := s.updateRepo.GetUpdateAssetMapping(ctx, *target)
	if err != nil {
		return fmt.Errorf("retrieving asset mapping of %s: %w", targetUpdateUUID, err)
	}
	targetBundle, err := s.readBundle(ctx, appId, targetMapping)
	if err != nil {
		return cancelIfPermanent(err)
	}
	if targetBundle == nil {
		// Legacy updates (not stored in CAS)
		return nil
	}
	sourceMapping, err := s.updateRepo.GetUpdateAssetMapping(ctx, *source)
	if err != nil {
		return fmt.Errorf("retrieving asset mapping of %s: %w", sourceUpdateUUID, err)
	}
	sourceBundle, err := s.readBundle(ctx, appId, sourceMapping)
	if err != nil {
		return cancelIfPermanent(err)
	}
	if sourceBundle == nil {
		// Legacy updates (not stored in CAS) or same bundle
		return nil
	}

	patch, err := bsdiff.Diff(sourceBundle, targetBundle)
	if err != nil {
		return river.JobCancel(fmt.Errorf("computing patch %s -> %s: %w", sourceUpdateUUID, targetUpdateUUID, err))
	}
	rebuilt, err := bsdiff.Patch(sourceBundle, patch)
	if err != nil || !bytes.Equal(rebuilt, targetBundle) {
		return river.JobCancel(fmt.Errorf("patch %s -> %s does not rebuild the target bundle: %v", sourceUpdateUUID, targetUpdateUUID, err))
	}

	fullDownload, err := gzippedSize(targetBundle)
	if err != nil {
		return err
	}
	if float64(len(patch)) > config.BundleDiffingPatchMaxRatio()*float64(fullDownload) {
		log.Printf("[bsdiff] patch %s -> %s not worth serving: %d bytes against a %d byte download", sourceUpdateUUID, targetUpdateUUID, len(patch), fullDownload)
		return nil
	}

	if err := s.bucket.PutBSDiff(ctx, appId, target.Branch, targetUpdateUUID, sourceUpdateUUID, bytes.NewReader(patch)); err != nil {
		return fmt.Errorf("storing patch %s -> %s: %w", sourceUpdateUUID, targetUpdateUUID, err)
	}
	log.Printf("[bsdiff] stored patch %s -> %s: %d bytes against a %d byte download", sourceUpdateUUID, targetUpdateUUID, len(patch), fullDownload)
	return nil
}

func cancelIfPermanent(err error) error {
	if errors.Is(err, errBundleTooLarge) || errors.Is(err, errBlobMissing) {
		return river.JobCancel(err)
	}
	return err
}
