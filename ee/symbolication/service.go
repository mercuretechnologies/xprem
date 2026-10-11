// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"xprem/ee/licensing"
	"xprem/internal/jobs"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// IndexStore holds the maps and their indexes.
type IndexStore interface {
	Get(ctx context.Context, appId, hash string) (*types.BucketFile, error)
	IndexExists(ctx context.Context, appId, hash string) (bool, error)
	GetIndex(ctx context.Context, appId, hash string) (*types.BucketFile, error)
	PutIndex(ctx context.Context, appId, hash string, body io.Reader) error
}

var (
	ErrUnavailable    = errors.New("source map indexing needs source map uploads, the control plane and an enterprise license")
	ErrNoSourcemap    = errors.New("this update has no source map")
	ErrIndexNotReady  = errors.New("the source map of this update is not indexed yet")
	ErrIndexFailed    = errors.New("the source map of this update could not be indexed")
	ErrUpdateNotFound = errors.New("update not found")
)

// maxMapSize bounds what one job decodes in memory.
const maxMapSize = 128 << 20

type indexJobQueue interface {
	Enqueue(ctx context.Context, args river.JobArgs) (string, error)
}

type Service struct {
	store        IndexStore
	indexes      IndexRepository
	jobs         indexJobQueue
	cache        *indexCache
	licenseValid func() bool
}

func NewService(store IndexStore, indexes IndexRepository, jobsClient *jobs.Client) *Service {
	var queue indexJobQueue
	if jobsClient != nil {
		queue = jobsClient
	}
	return &Service{store: store, indexes: indexes, jobs: queue, cache: newIndexCache(), licenseValid: licensing.IsEnterprise}
}

// available is nil-safe: the handler is wired even when indexing is not.
func (s *Service) available() bool {
	return s != nil && s.store != nil && s.indexes != nil && s.jobs != nil && s.licenseValid()
}

// Available reports feature and license availability without reading an index.
func (s *Service) Available() bool { return s.available() }

// GetUpdateSourcemap answers ErrNoSourcemap for an update published without a map.
func (s *Service) GetUpdateSourcemap(ctx context.Context, appId, branch, runtimeVersion, updateId string) (*UpdateSourcemap, error) {
	if !s.available() {
		return nil, ErrUnavailable
	}
	if err := validation.Name("branchName", branch); err != nil {
		return nil, err
	}
	if err := validation.Name("updateId", updateId); err != nil {
		return nil, err
	}
	sourcemap, err := s.indexes.GetUpdateSourcemap(ctx, appId, branch, runtimeVersion, updateId)
	if err != nil {
		return nil, err
	}
	if sourcemap == nil {
		return nil, ErrUpdateNotFound
	}
	if sourcemap.Hash == nil {
		return nil, ErrNoSourcemap
	}
	return sourcemap, nil
}

// Reindex schedules the index of an update's map again, as its publish did.
func (s *Service) Reindex(ctx context.Context, appId, branch, runtimeVersion, updateId string) error {
	sourcemap, err := s.GetUpdateSourcemap(ctx, appId, branch, runtimeVersion, updateId)
	if err != nil {
		return err
	}
	update := types.Update{AppId: appId, Branch: branch, RuntimeVersion: runtimeVersion, UpdateId: updateId}
	return s.scheduleIndex(ctx, update, *sourcemap.Hash, true)
}

const indexJobKind = "sourcemap-index"

// indexArgs names the update for uniqueness and the record, and carries the
// map hash so the worker needs no lookup to start.
type indexArgs struct {
	AppId          string `json:"appId" river:"unique"`
	Branch         string `json:"branch" river:"unique"`
	UpdateId       string `json:"updateId" river:"unique"`
	RuntimeVersion string `json:"runtimeVersion"`
	Hash           string `json:"hash"`
	// Rebuild writes the index even when the store already holds one.
	Rebuild bool `json:"rebuild"`
}

func (indexArgs) Kind() string { return indexJobKind }

func (indexArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       jobs.QueueSourcemapIndex,
		MaxAttempts: 5,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			// Unique only while the job is alive: a finished map can be reindexed.
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

type indexWorker struct {
	river.WorkerDefaults[indexArgs]
	service *Service
}

func (w *indexWorker) Work(ctx context.Context, job *river.Job[indexArgs]) error {
	return w.service.runIndexJob(ctx, job)
}

func RegisterWorker(workers *river.Workers, service *Service) {
	river.AddWorker(workers, &indexWorker{service: service})
}

// ScheduleIndex records the update's map as pending and queues its index job.
// A no-op when indexing is unavailable.
func (s *Service) ScheduleIndex(ctx context.Context, update types.Update, hash string) error {
	return s.scheduleIndex(ctx, update, hash, false)
}

func (s *Service) scheduleIndex(ctx context.Context, update types.Update, hash string, rebuild bool) error {
	if !s.available() {
		return nil
	}
	// A publish records the job as queued; a rebuild leaves the record to the
	// worker, so a refused one changes nothing.
	if !rebuild {
		if err := s.indexes.MarkPending(ctx, update, hash); err != nil {
			return err
		}
	}
	_, err := s.jobs.Enqueue(ctx, indexArgs{
		AppId:          update.AppId,
		Branch:         update.Branch,
		UpdateId:       update.UpdateId,
		RuntimeVersion: update.RuntimeVersion,
		Hash:           hash,
		Rebuild:        rebuild,
	})
	if errors.Is(err, jobs.ErrAlreadyRunning) {
		// A publish is content with the running job; a reindex is not.
		if rebuild {
			return err
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue sourcemap index of update %s: %w", update.UpdateId, err)
	}
	return nil
}

// indexOutcome is how a job ended without an error; indexSize is nil for an
// index another update already built.
type indexOutcome struct {
	segments  int
	indexSize *int64
}

// runIndexJob wraps buildIndex with the sourcemap_indexes bookkeeping.
func (s *Service) runIndexJob(ctx context.Context, job *river.Job[indexArgs]) error {
	args := job.Args
	update := types.Update{AppId: args.AppId, Branch: args.Branch, RuntimeVersion: args.RuntimeVersion, UpdateId: args.UpdateId}
	s.record(update, func() error { return s.indexes.MarkRunning(ctx, update, args.Hash, job.Attempt) })

	outcome, err := s.buildIndex(ctx, args.AppId, args.Hash, args.Rebuild)
	if err == nil {
		// Not through record: an index without its stored record is not usable yet.
		segments := outcome.segments
		if err := s.indexes.Finish(ctx, update, types.SourcemapIndexStored, "", &segments, outcome.indexSize); err != nil {
			return fmt.Errorf("recording the index of update %s: %w", update.UpdateId, err)
		}
		return nil
	}
	var cancel *river.JobCancelError
	// A failure River will retry waits for its next attempt.
	status := types.SourcemapIndexPending
	reason := err.Error()
	switch {
	case errors.As(err, &cancel):
		status = types.SourcemapIndexCancelled
		// The record keeps the reason code in front, not River's prefix.
		if inner := errors.Unwrap(cancel); inner != nil {
			reason = inner.Error()
		}
	case job.Attempt >= job.MaxAttempts:
		status = types.SourcemapIndexFailed
	}
	s.record(update, func() error { return s.indexes.Finish(ctx, update, status, reason, nil, nil) })
	return err
}

// record runs a bookkeeping write and logs its failure instead of failing the job.
func (s *Service) record(update types.Update, write func() error) {
	if err := write(); err != nil {
		log.Printf("[sourcemap] cannot record the index of update %s: %v", update.UpdateId, err)
	}
}

// buildIndex writes the index of a map unless the store already holds one and
// no rebuild was asked. What retrying cannot fix is a JobCancelError.
func (s *Service) buildIndex(ctx context.Context, appId, hash string, rebuild bool) (indexOutcome, error) {
	// Checked again here: the job may have been queued under a license that lapsed since.
	if !s.licenseValid() {
		return indexOutcome{}, river.JobCancel(fmt.Errorf("%s: %v", types.SourcemapIndexReasonUnavailable, ErrUnavailable))
	}
	if !rebuild {
		exists, err := s.store.IndexExists(ctx, appId, hash)
		if err != nil {
			return indexOutcome{}, fmt.Errorf("checking the index of map %s: %w", hash, err)
		}
		if exists {
			outcome, err := s.existingIndex(ctx, appId, hash)
			// An index of an unsupported format is written again.
			if !errors.Is(err, ErrInvalidIndex) {
				return outcome, err
			}
		}
	}
	file, err := s.store.Get(ctx, appId, hash)
	if err != nil {
		return indexOutcome{}, fmt.Errorf("reading map %s: %w", hash, err)
	}
	if file == nil {
		return indexOutcome{}, river.JobCancel(fmt.Errorf("%s: map %s is not in the store", types.SourcemapIndexReasonMapMissing, hash))
	}
	defer file.Reader.Close()
	data, err := io.ReadAll(io.LimitReader(file.Reader, maxMapSize+1))
	if err != nil {
		return indexOutcome{}, fmt.Errorf("reading map %s: %w", hash, err)
	}
	if len(data) > maxMapSize {
		return indexOutcome{}, river.JobCancel(fmt.Errorf("%s: map %s exceeds %d MB", types.SourcemapIndexReasonMapTooLarge, hash, maxMapSize>>20))
	}
	m, err := parseMap(ctx, data, maxIndexCacheBytes)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return indexOutcome{}, err
		}
		if errors.Is(err, errSegmentLimit) {
			return indexOutcome{}, river.JobCancel(fmt.Errorf("%s: %v", types.SourcemapIndexReasonIndexTooLarge, err))
		}
		return indexOutcome{}, river.JobCancel(fmt.Errorf("%s: %v", types.SourcemapIndexReasonMapInvalid, err))
	}
	if IndexSize(m) > maxIndexCacheBytes {
		return indexOutcome{}, river.JobCancel(fmt.Errorf("%s: the index of map %s exceeds %d MB", types.SourcemapIndexReasonIndexTooLarge, hash, maxIndexCacheBytes>>20))
	}
	var index bytes.Buffer
	if err := WriteIndex(&index, m); err != nil {
		return indexOutcome{}, river.JobCancel(fmt.Errorf("%s: %v", types.SourcemapIndexReasonMapInvalid, err))
	}
	if err := s.store.PutIndex(ctx, appId, hash, bytes.NewReader(index.Bytes())); err != nil {
		return indexOutcome{}, fmt.Errorf("storing the index of map %s: %w", hash, err)
	}
	size := int64(index.Len())
	return indexOutcome{segments: len(m.Segments), indexSize: &size}, nil
}

// existingIndex describes an index another update of the same map already
// built.
func (s *Service) existingIndex(ctx context.Context, appId, hash string) (indexOutcome, error) {
	file, err := s.store.GetIndex(ctx, appId, hash)
	if err != nil {
		return indexOutcome{}, fmt.Errorf("reading the index of map %s: %w", hash, err)
	}
	if file == nil {
		return indexOutcome{}, fmt.Errorf("the index of map %s is not in the store", hash)
	}
	defer file.Reader.Close()
	segments, err := ReadSegmentCount(file.Reader)
	if err != nil {
		return indexOutcome{}, err
	}
	return indexOutcome{segments: segments}, nil
}

// OpenUpdateIndex opens the index of the update a device reports by UUID.
// An update whose index is not there yet is ErrIndexNotReady, one that never
// will have one is ErrNoSourcemap or ErrIndexFailed.
func (s *Service) OpenUpdateIndex(ctx context.Context, appId, updateUUID string) (*Index, error) {
	if !s.available() {
		return nil, ErrUnavailable
	}
	sourcemap, err := s.storedUpdateSourcemap(ctx, appId, updateUUID)
	if err != nil {
		return nil, err
	}
	hash := *sourcemap.Hash
	if index, ok := s.cache.get(appId + "/" + hash); ok {
		return index, nil
	}
	file, err := s.store.GetIndex(ctx, appId, hash)
	if err != nil {
		return nil, fmt.Errorf("reading the index of map %s: %w", hash, err)
	}
	if file == nil {
		return nil, s.repairIndex(ctx, sourcemap)
	}
	defer file.Reader.Close()
	data, err := io.ReadAll(io.LimitReader(file.Reader, maxIndexCacheBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading the index of map %s: %w", hash, err)
	}
	if len(data) > maxIndexCacheBytes {
		return nil, fmt.Errorf("%w: the index of map %s exceeds %d MB", ErrIndexFailed, hash, maxIndexCacheBytes>>20)
	}
	index, err := s.cache.put(appId+"/"+hash, data)
	if errors.Is(err, ErrInvalidIndex) {
		return nil, s.repairIndex(ctx, sourcemap)
	}
	return index, err
}

func (s *Service) repairIndex(ctx context.Context, sourcemap *UpdateSourcemap) error {
	err := s.scheduleIndex(ctx, sourcemap.Update, *sourcemap.Hash, true)
	if err != nil && !errors.Is(err, jobs.ErrAlreadyRunning) {
		return err
	}
	return ErrIndexNotReady
}

// UpdateIndexState verifies the stored index and queues a rebuild if needed.
func (s *Service) UpdateIndexState(ctx context.Context, appId, updateUUID string) error {
	_, err := s.OpenUpdateIndex(ctx, appId, updateUUID)
	return err
}

func (s *Service) storedUpdateSourcemap(ctx context.Context, appId, updateUUID string) (*UpdateSourcemap, error) {
	sourcemap, err := s.indexes.GetUpdateSourcemapByUUID(ctx, appId, updateUUID)
	if err != nil {
		return nil, err
	}
	if sourcemap == nil {
		return nil, ErrUpdateNotFound
	}
	if sourcemap.Hash == nil {
		return nil, ErrNoSourcemap
	}
	if sourcemap.Index == nil {
		return nil, ErrIndexNotReady
	}
	switch sourcemap.Index.Status {
	case types.SourcemapIndexStored:
		return sourcemap, nil
	case types.SourcemapIndexFailed, types.SourcemapIndexCancelled:
		return nil, ErrIndexFailed
	}
	return nil, ErrIndexNotReady
}
