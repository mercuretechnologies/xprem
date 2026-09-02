package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	bucket 		  bucket.Bucket
	jobs 		  *jobs.Client
	updateService *UpdateService  
	updateRepo    UpdateRepository
}

func NewBSDiffService(bucket bucket.Bucket, jobsClient *jobs.Client, updateService *UpdateService, updateRepo UpdateRepository) *BsDiffService {
	return &BsDiffService {
		bucket: bucket,
		jobs: jobsClient,
		updateService: updateService,
		updateRepo: updateRepo,
	}
}

const bsDiffComputeJobKind = "bsdiff-compute"

type bsDiffComputeArgs struct {
	TargetUpdateUUID 	string `json:"targetUpdateUUID" river:"unique"`
	SourceUpdateUUID 	string `json:"sourceUpdateUUID" river:"unique"`
	AppId           	string `json:"appId" river:"unique"`
}

func (bsDiffComputeArgs) Kind() string { return bsDiffComputeJobKind }

func (bsDiffComputeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
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
	if !config.IsDBMode() {
		log.Printf("[bsdiff] no patches for update %s: bundle patches need the control plane (database mode)", update.UpdateId)
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

func (s *BsDiffService) retrieveBundleBytes(ctx context.Context, appId string, launchAsset types.ShapedAsset) ([]byte, error) {
      if launchAsset.Hash != "" {
              blob, err := s.bucket.GetBlob(ctx, appId, launchAsset.Hash)
              if err != nil {
                      return nil, fmt.Errorf("reading blob %s: %w", launchAsset.Hash, err)
              }
              if blob != nil {
                      return bucket.ConvertReadCloserToBytes(blob.Reader)
              }
      }
	  return nil, nil
}

func (s *BsDiffService) computeBSDiff(ctx context.Context, appId, targetUpdateUUID, sourceUpdateUUID string) error {
	targetUpdate, err := s.updateRepo.GetUpdateByUUID(ctx, appId, targetUpdateUUID)
	if err != nil {
		return fmt.Errorf("failed while retrieving target update %s: %w", targetUpdateUUID, err)
	}
	if targetUpdate == nil {
		return fmt.Errorf("target update %s not found", targetUpdateUUID)
	}
	sourceUpdate, err := s.updateRepo.GetUpdateByUUID(ctx, appId, sourceUpdateUUID)
	if err != nil {
		return fmt.Errorf("failed while retrieving source update %s: %w", sourceUpdateUUID, err)
	}
	if sourceUpdate == nil {
		return fmt.Errorf("source update %s not found", sourceUpdateUUID)
	}
	targetAssetsMapping, err := s.updateRepo.GetUpdateAssetMapping(ctx, *targetUpdate)
	if err != nil {
		return fmt.Errorf("failed while retrieving target update assets mapping %s: %w", targetUpdateUUID, err)
	}
	targetBundleBytes, err := s.retrieveBundleBytes(ctx, appId, targetAssetsMapping.LaunchAsset)
	if err != nil {
		return fmt.Errorf("failed while retrieving target update bundle %s: %w", targetUpdateUUID, err)
	}
	if targetBundleBytes == nil {
		return nil
	}
	sourceAssetsMapping, err := s.updateRepo.GetUpdateAssetMapping(ctx, *sourceUpdate)
	if err != nil {
		return fmt.Errorf("failed while retrieving source update assets mapping %s: %w", sourceUpdateUUID, err)
	}
	sourceBundleBytes, err := s.retrieveBundleBytes(ctx, appId, sourceAssetsMapping.LaunchAsset)
	if err != nil {
		return fmt.Errorf("failed while retrieving source update bundle %s: %w", sourceUpdateUUID, err)
	}
	if sourceBundleBytes == nil {
		return nil
	}
	computedBsDiff, err := bsdiff.Diff(sourceBundleBytes, targetBundleBytes)
	if err != nil {
		return fmt.Errorf("failed while computing bsdiff: %w", err)
	}
	err = s.bucket.PutBSDiff(ctx, appId, targetUpdate.UpdateId, sourceUpdate.UpdateId, bytes.NewReader(computedBsDiff))
	if err != nil {
		return fmt.Errorf("error while uploading bsdiff in bucket: %w", err)
	}
	return nil
}