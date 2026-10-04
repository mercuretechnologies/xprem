// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"xprem/internal/jobs"
	"xprem/internal/types"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore keeps maps and indexes in memory; getErr makes every read fail.
type fakeStore struct {
	maps    map[string][]byte
	indexes map[string][]byte
	getErr  error
	puts    int
}

func newFakeStore() *fakeStore {
	return &fakeStore{maps: map[string][]byte{}, indexes: map[string][]byte{}}
}

func (s *fakeStore) Get(_ context.Context, _, hash string) (*types.BucketFile, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	data, ok := s.maps[hash]
	if !ok {
		return nil, nil
	}
	return &types.BucketFile{Reader: io.NopCloser(bytes.NewReader(data))}, nil
}

func (s *fakeStore) IndexExists(_ context.Context, _, hash string) (bool, error) {
	_, ok := s.indexes[hash]
	return ok, nil
}

func (s *fakeStore) GetIndex(_ context.Context, _, hash string) (*types.BucketFile, error) {
	data, ok := s.indexes[hash]
	if !ok {
		return nil, nil
	}
	return &types.BucketFile{Reader: io.NopCloser(bytes.NewReader(data))}, nil
}

func (s *fakeStore) PutIndex(_ context.Context, _, hash string, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.indexes[hash] = data
	s.puts++
	return nil
}

// fakeIndexes records the last status written for the update; finishErr makes
// every Finish fail.
type fakeIndexes struct {
	update    types.Update
	record    *types.SourcemapIndex
	finishErr error
}

func (r *fakeIndexes) MarkPending(_ context.Context, _ types.Update, hash string) error {
	r.record = &types.SourcemapIndex{Hash: hash, Status: types.SourcemapIndexPending}
	return nil
}

func (r *fakeIndexes) MarkRunning(_ context.Context, _ types.Update, hash string, attempt int) error {
	if r.record == nil {
		r.record = &types.SourcemapIndex{Hash: hash}
	}
	r.record.Status, r.record.Attempts = types.SourcemapIndexRunning, attempt
	return nil
}

func (r *fakeIndexes) Finish(_ context.Context, _ types.Update, status types.SourcemapIndexStatus, reason string, segments *int, indexSize *int64) error {
	if r.finishErr != nil {
		return r.finishErr
	}
	r.record.Status, r.record.Reason, r.record.Segments, r.record.IndexSize = status, reason, segments, indexSize
	return nil
}

func (r *fakeIndexes) GetUpdateSourcemapByUUID(context.Context, string, string) (*UpdateSourcemap, error) {
	if r.record == nil {
		return &UpdateSourcemap{}, nil
	}
	return &UpdateSourcemap{Hash: &r.record.Hash, Index: r.record, Update: r.update}, nil
}

func (r *fakeIndexes) GetUpdateSourcemap(context.Context, string, string, string, string) (*UpdateSourcemap, error) {
	if r.record == nil {
		return &UpdateSourcemap{}, nil
	}
	return &UpdateSourcemap{Hash: &r.record.Hash, Index: r.record}, nil
}

const testHash = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"

func newTestService(store *fakeStore) (*Service, *fakeIndexes) {
	indexes := &fakeIndexes{update: types.Update{AppId: "app-1", Branch: "main", RuntimeVersion: "1", UpdateId: "100"}}
	service := &Service{store: store, indexes: indexes, cache: newIndexCache(), licenseValid: func() bool { return true }}
	return service, indexes
}

func runJob(t *testing.T, service *Service, indexes *fakeIndexes, attempt int) error {
	t.Helper()
	return runJobArgs(t, service, indexes, attempt, false)
}

func runJobArgs(t *testing.T, service *Service, indexes *fakeIndexes, attempt int, rebuild bool) error {
	t.Helper()
	update := types.Update{AppId: "app-1", Branch: "main", RuntimeVersion: "1", UpdateId: "100"}
	require.NoError(t, indexes.MarkPending(context.Background(), update, testHash))
	job := &river.Job[indexArgs]{
		JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: 5},
		Args:   indexArgs{AppId: "app-1", Branch: "main", UpdateId: "100", RuntimeVersion: "1", Hash: testHash, Rebuild: rebuild},
	}
	return service.runIndexJob(context.Background(), job)
}

func TestIndexJobStoresTheIndex(t *testing.T) {
	store := newFakeStore()
	store.maps[testHash] = []byte(cartMap)
	service, indexes := newTestService(store)

	require.NoError(t, runJob(t, service, indexes, 1))
	assert.Equal(t, types.SourcemapIndexStored, indexes.record.Status)
	require.NotNil(t, indexes.record.Segments)
	assert.Equal(t, 36, *indexes.record.Segments)
	require.NotNil(t, indexes.record.IndexSize)
	assert.EqualValues(t, len(store.indexes[testHash]), *indexes.record.IndexSize)

	index, err := OpenIndex(bytes.NewReader(store.indexes[testHash]))
	require.NoError(t, err)
	pos, ok, err := index.Lookup(0, 40)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "cart.js", pos.Source)
}

// A republish shares its map with the original update: the index is not
// rebuilt, the record still says what it holds.
func TestIndexJobReusesAnExistingIndex(t *testing.T) {
	store := newFakeStore()
	m, err := parseMap(context.Background(), []byte(cartMap), maxIndexCacheBytes)
	require.NoError(t, err)
	var existing bytes.Buffer
	require.NoError(t, WriteIndex(&existing, m))
	store.indexes[testHash] = existing.Bytes()
	service, indexes := newTestService(store)

	require.NoError(t, runJob(t, service, indexes, 1))
	assert.Equal(t, types.SourcemapIndexStored, indexes.record.Status)
	assert.Equal(t, 0, store.puts, "nothing written")
	require.NotNil(t, indexes.record.Segments)
	assert.Equal(t, 36, *indexes.record.Segments)
	assert.Nil(t, indexes.record.IndexSize, "the size of a reused index is not known")

	// A reindex rebuilds it regardless.
	store.maps[testHash] = []byte(cartMap)
	require.NoError(t, runJobArgs(t, service, indexes, 1, true))
	assert.Equal(t, 1, store.puts)
	require.NotNil(t, indexes.record.IndexSize)
}

func TestIndexJobCancelsOnPermanentConditions(t *testing.T) {
	for name, tc := range map[string]struct {
		mapData []byte
		reason  string
	}{
		"missing map":   {nil, types.SourcemapIndexReasonMapMissing},
		"invalid map":   {[]byte("\xef\xbb\xbf" + cartMap), types.SourcemapIndexReasonMapInvalid},
		"map too large": {[]byte(strings.Repeat(" ", maxMapSize+1)), types.SourcemapIndexReasonMapTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			if tc.mapData != nil {
				store.maps[testHash] = tc.mapData
			}
			service, indexes := newTestService(store)

			err := runJob(t, service, indexes, 1)
			var cancel *river.JobCancelError
			require.ErrorAs(t, err, &cancel, "no retry can fix this")
			assert.Equal(t, types.SourcemapIndexCancelled, indexes.record.Status)
			assert.True(t, strings.HasPrefix(indexes.record.Reason, tc.reason+":"), indexes.record.Reason)
			assert.Empty(t, store.indexes)
		})
	}
}

// A store that cannot be read is retried; the record turns failed only when
// the attempts run out.
func TestIndexJobRetriesTransientErrors(t *testing.T) {
	store := newFakeStore()
	store.getErr = errors.New("connection reset")
	service, indexes := newTestService(store)

	err := runJob(t, service, indexes, 1)
	require.Error(t, err)
	var cancel *river.JobCancelError
	assert.False(t, errors.As(err, &cancel))
	assert.Equal(t, types.SourcemapIndexPending, indexes.record.Status, "waits for River's next attempt")

	require.Error(t, runJob(t, service, indexes, 5))
	assert.Equal(t, types.SourcemapIndexFailed, indexes.record.Status)
	assert.Contains(t, indexes.record.Reason, "connection reset")
}

// An index whose stored record cannot be written is not usable yet: the job
// fails so River retries, and the retry records the index without rebuilding it.
func TestIndexJobFailsUntilTheStoredRecordIsWritten(t *testing.T) {
	store := newFakeStore()
	store.maps[testHash] = []byte(cartMap)
	service, indexes := newTestService(store)
	indexes.finishErr = errors.New("connection reset")

	err := runJob(t, service, indexes, 1)
	require.ErrorContains(t, err, "connection reset")
	assert.Equal(t, 1, store.puts, "the index itself was stored")
	assert.Equal(t, types.SourcemapIndexRunning, indexes.record.Status)
	_, err = service.storedUpdateSourcemap(context.Background(), "app-1", "update-uuid")
	assert.ErrorIs(t, err, ErrIndexNotReady)

	indexes.finishErr = nil
	require.NoError(t, runJob(t, service, indexes, 2))
	assert.Equal(t, types.SourcemapIndexStored, indexes.record.Status)
	assert.Equal(t, 1, store.puts, "the retry reuses the stored index")
	sourcemap, err := service.storedUpdateSourcemap(context.Background(), "app-1", "update-uuid")
	require.NoError(t, err)
	assert.Equal(t, testHash, *sourcemap.Hash)
}

func TestIndexingIsUnavailableWithoutALicense(t *testing.T) {
	service, _ := newTestService(newFakeStore())
	service.jobs = &jobs.Client{}
	require.True(t, service.available())
	service.licenseValid = func() bool { return false }
	assert.False(t, service.available())
	_, err := service.GetUpdateSourcemap(context.Background(), "app-1", "main", "1", "100")
	assert.ErrorIs(t, err, ErrUnavailable)
}

// A job queued under a license that lapsed since is cancelled, not run.
func TestIndexJobIsCancelledOnceTheLicenseLapsed(t *testing.T) {
	store := newFakeStore()
	store.maps[testHash] = []byte(cartMap)
	service, indexes := newTestService(store)
	service.licenseValid = func() bool { return false }

	err := runJob(t, service, indexes, 1)
	var cancel *river.JobCancelError
	require.ErrorAs(t, err, &cancel)
	assert.Equal(t, types.SourcemapIndexCancelled, indexes.record.Status)
	assert.True(t, strings.HasPrefix(indexes.record.Reason, types.SourcemapIndexReasonUnavailable), indexes.record.Reason)
	assert.Equal(t, 0, store.puts, "nothing is written without a license")
}

// Without the job client, indexing is off: scheduling records nothing.
func TestScheduleIndexIsANoOpWhenUnavailable(t *testing.T) {
	service, indexes := newTestService(newFakeStore())
	require.NoError(t, service.ScheduleIndex(context.Background(), types.Update{AppId: "app-1", Branch: "main", UpdateId: "100"}, testHash))
	assert.Nil(t, indexes.record)
}

type fakeIndexQueue struct {
	mu       sync.Mutex
	accepted []indexArgs
	active   map[string]bool
	attempts int
	err      error
}

func (q *fakeIndexQueue) Enqueue(_ context.Context, jobArgs river.JobArgs) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.attempts++
	if q.err != nil {
		return "", q.err
	}
	args := jobArgs.(indexArgs)
	key := args.AppId + "/" + args.Branch + "/" + args.UpdateId
	if q.active[key] {
		return "", jobs.ErrAlreadyRunning
	}
	if q.active == nil {
		q.active = make(map[string]bool)
	}
	q.active[key] = true
	q.accepted = append(q.accepted, args)
	return "job", nil
}

func storedTestService(store *fakeStore) (*Service, *fakeIndexes, *fakeIndexQueue) {
	service, indexes := newTestService(store)
	indexes.record = &types.SourcemapIndex{Hash: testHash, Status: types.SourcemapIndexStored}
	queue := &fakeIndexQueue{}
	service.jobs = queue
	return service, indexes, queue
}

func TestOpenUpdateIndexRepairsMissingAndInvalidIndexes(t *testing.T) {
	for _, condition := range []string{"missing", "unsupported version", "corrupt"} {
		t.Run(condition, func(t *testing.T) {
			store := newFakeStore()
			store.maps[testHash] = []byte(cartMap)
			if condition == "unsupported version" {
				m, err := parseMap(context.Background(), store.maps[testHash], maxIndexCacheBytes)
				require.NoError(t, err)
				var encoded bytes.Buffer
				require.NoError(t, WriteIndex(&encoded, m))
				data := encoded.Bytes()
				le.PutUint32(data[4:], 1)
				store.indexes[testHash] = data
			} else if condition == "corrupt" {
				store.indexes[testHash] = []byte("truncated")
			}
			service, indexes, queue := storedTestService(store)
			ctx := context.Background()
			index, err := service.OpenUpdateIndex(ctx, "app-1", "update-uuid")
			require.ErrorIs(t, err, ErrIndexNotReady)
			require.Nil(t, index)
			require.Len(t, queue.accepted, 1)
			args := queue.accepted[0]
			assert.Equal(t, "app-1", args.AppId)
			assert.Equal(t, "main", args.Branch)
			assert.Equal(t, "1", args.RuntimeVersion)
			assert.Equal(t, "100", args.UpdateId)
			assert.Equal(t, testHash, args.Hash)
			assert.True(t, args.Rebuild)
			assert.Equal(t, types.SourcemapIndexStored, indexes.record.Status, "enqueue does not race the worker's status writes")

			require.ErrorIs(t, service.UpdateIndexState(ctx, "app-1", "update-uuid"), ErrIndexNotReady)
			require.Len(t, queue.accepted, 1)
			require.NoError(t, service.runIndexJob(ctx, &river.Job[indexArgs]{
				JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: 5}, Args: args,
			}))
			assert.Equal(t, types.SourcemapIndexStored, indexes.record.Status)
			assert.Equal(t, []byte(cartMap), store.maps[testHash], "rebuilding preserves the uploaded source map")
			index, err = service.OpenUpdateIndex(ctx, "app-1", "update-uuid")
			require.NoError(t, err)
			require.NotNil(t, index)
			position, ok, err := index.Lookup(0, 40)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, "cart.js", position.Source)
			require.NoError(t, service.UpdateIndexState(ctx, "app-1", "update-uuid"))
			require.Len(t, queue.accepted, 1)
			assert.Equal(t, 1, store.puts)
		})
	}
}

func TestIndexStateTriggersRepairAndConcurrentReadsDeduplicate(t *testing.T) {
	service, indexes, queue := storedTestService(newFakeStore())
	ctx := context.Background()
	require.ErrorIs(t, service.UpdateIndexState(ctx, "app-1", "update-uuid"), ErrIndexNotReady)
	const readers = 16
	errs := make(chan error, readers)
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.OpenUpdateIndex(ctx, "app-1", "update-uuid")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.ErrorIs(t, err, ErrIndexNotReady)
	}
	require.Len(t, queue.accepted, 1)
	assert.Equal(t, readers+1, queue.attempts)
	assert.Equal(t, types.SourcemapIndexStored, indexes.record.Status)
}

func TestRepairEnqueueFailureLeavesStoredRecordRetryable(t *testing.T) {
	service, indexes, queue := storedTestService(newFakeStore())
	queue.err = errors.New("queue unavailable")
	_, err := service.OpenUpdateIndex(context.Background(), "app-1", "update-uuid")
	require.ErrorIs(t, err, queue.err)
	assert.Equal(t, types.SourcemapIndexStored, indexes.record.Status)
	assert.Empty(t, queue.accepted)
	queue.err = nil
	_, err = service.OpenUpdateIndex(context.Background(), "app-1", "update-uuid")
	require.ErrorIs(t, err, ErrIndexNotReady)
	require.Len(t, queue.accepted, 1)
}

func TestRepairWithMissingSourceMapStopsAfterWorkerCancellation(t *testing.T) {
	service, indexes, queue := storedTestService(newFakeStore())
	ctx := context.Background()
	_, err := service.OpenUpdateIndex(ctx, "app-1", "update-uuid")
	require.ErrorIs(t, err, ErrIndexNotReady)
	require.Len(t, queue.accepted, 1)
	err = service.runIndexJob(ctx, &river.Job[indexArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: 5}, Args: queue.accepted[0],
	})
	var cancel *river.JobCancelError
	require.ErrorAs(t, err, &cancel)
	assert.Equal(t, types.SourcemapIndexCancelled, indexes.record.Status)
	assert.Contains(t, indexes.record.Reason, types.SourcemapIndexReasonMapMissing)
	_, err = service.OpenUpdateIndex(ctx, "app-1", "update-uuid")
	require.ErrorIs(t, err, ErrIndexFailed)
	require.ErrorIs(t, service.UpdateIndexState(ctx, "app-1", "update-uuid"), ErrIndexFailed)
	assert.Equal(t, 1, queue.attempts, "a terminal map failure must not start another repair on every read")
}

func TestIndexRepairRequiresAvailableService(t *testing.T) {
	service, _, queue := storedTestService(newFakeStore())
	service.licenseValid = func() bool { return false }
	_, err := service.OpenUpdateIndex(context.Background(), "app-1", "update-uuid")
	require.ErrorIs(t, err, ErrUnavailable)
	assert.Zero(t, queue.attempts)

	// A nil client must leave the queue interface nil.
	withoutJobs := NewService(newFakeStore(), &fakeIndexes{}, nil)
	withoutJobs.licenseValid = func() bool { return true }
	assert.False(t, withoutJobs.available())
}
