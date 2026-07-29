// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package audit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"xprem/internal/auditlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePutter struct {
	objects map[string]string
	putErr  error
}

func (f *fakePutter) PutObject(_ context.Context, key string, body []byte) error {
	if f.putErr != nil {
		return f.putErr
	}
	if f.objects == nil {
		f.objects = map[string]string{}
	}
	f.objects[key] = string(body)
	return nil
}

func seededExportRepo() *fakeAuditRepo {
	occurred := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	// Newest first, like every other listResult seed.
	return &fakeAuditRepo{listResult: []Event{
		{ID: 3, OccurredAt: occurred, Action: auditlog.ActionUserLogin, ActorType: auditlog.ActorUser, ActorID: "u-1", ActorDisplay: "axel@example.com", TargetType: "user", TargetID: "u-1", Outcome: auditlog.OutcomeSuccess},
		{ID: 2, OccurredAt: occurred, Action: auditlog.ActionAppRenamed, ActorType: auditlog.ActorUser, ActorID: "u-1", ActorDisplay: "axel@example.com", TargetType: "app", TargetID: "app-1", AppID: "app-1", Outcome: auditlog.OutcomeSuccess, Metadata: map[string]any{"previous_name": "Old"}},
		{ID: 1, OccurredAt: occurred, Action: auditlog.ActionUserLogin, ActorType: auditlog.ActorUser, ActorID: "u-1", ActorDisplay: "axel@example.com", TargetType: "user", TargetID: "u-1", Outcome: auditlog.OutcomeFailure},
	}}
}

func TestArchiveExportsBatchesAndAdvancesTheCursor(t *testing.T) {
	repo := seededExportRepo()
	putter := &fakePutter{}
	service := enabledService(repo)

	exported, err := service.archiveNextBatch(context.Background(), putter)
	require.NoError(t, err)
	// Fewer rows than a full batch: nothing more is waiting.
	require.False(t, exported)
	require.EqualValues(t, 3, repo.exportCursor)

	require.Len(t, putter.objects, 1)
	body, ok := putter.objects["2026/07/22/1-3.ndjson"]
	require.True(t, ok, "file key must be YYYY/MM/DD/<firstId>-<lastId>.ndjson, got %v", putter.objects)

	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	require.Len(t, lines, 3)
	// Same vocabulary as the HTTP API, one JSON object per line.
	assert.Contains(t, lines[0], `"id":1`)
	assert.Contains(t, lines[0], `"occurredAt":"2026-07-22T09:00:00Z"`)
	assert.Contains(t, lines[0], `"outcome":"failure"`)
	assert.Contains(t, lines[1], `"metadata":{"previous_name":"Old"}`)

	// A second pass has nothing left: no new file, cursor untouched.
	exported, err = service.archiveNextBatch(context.Background(), putter)
	require.NoError(t, err)
	require.False(t, exported)
	require.Len(t, putter.objects, 1)
}

func TestArchiveKeysAMultiDayBatchUnderItsFirstEventDate(t *testing.T) {
	// A batch straddling UTC midnight: the file key must come from the FIRST
	// event's date, whatever days the rest of the batch spills into.
	beforeMidnight := time.Date(2026, 7, 21, 23, 59, 0, 0, time.UTC)
	afterMidnight := time.Date(2026, 7, 22, 0, 1, 0, 0, time.UTC)
	repo := &fakeAuditRepo{listResult: []Event{
		{ID: 2, OccurredAt: afterMidnight, Action: auditlog.ActionUserLogin, ActorType: auditlog.ActorUser, ActorID: "u-1", ActorDisplay: "axel@example.com", TargetType: "user", TargetID: "u-1", Outcome: auditlog.OutcomeSuccess},
		{ID: 1, OccurredAt: beforeMidnight, Action: auditlog.ActionUserLogin, ActorType: auditlog.ActorUser, ActorID: "u-1", ActorDisplay: "axel@example.com", TargetType: "user", TargetID: "u-1", Outcome: auditlog.OutcomeSuccess},
	}}
	putter := &fakePutter{}
	service := enabledService(repo)

	_, err := service.archiveNextBatch(context.Background(), putter)
	require.NoError(t, err)
	require.Contains(t, putter.objects, "2026/07/21/1-2.ndjson")
}

func TestArchiveLoopsUntilTheBacklogIsDrained(t *testing.T) {
	previousBatchSize := exportBatchSize
	exportBatchSize = 2
	t.Cleanup(func() { exportBatchSize = previousBatchSize })

	repo := seededExportRepo()
	putter := &fakePutter{}
	service := enabledService(repo)

	service.runArchive(context.Background(), putter)

	// 3 events, batches of 2: two files, cursor at the end.
	require.Len(t, putter.objects, 2)
	require.Contains(t, putter.objects, "2026/07/22/1-2.ndjson")
	require.Contains(t, putter.objects, "2026/07/22/3-3.ndjson")
	require.EqualValues(t, 3, repo.exportCursor)
}

func TestArchiveKeepsDrainingWithoutALicense(t *testing.T) {
	// Same rule as the viewer reads: a lapsed license stops collection, never
	// access to what was collected while licensed. Gating the archive would
	// strand unexported rows forever (the purge spares them, the exporter
	// would never come for them).
	repo := seededExportRepo()
	putter := &fakePutter{}
	service := NewAuditService(repo)
	service.licenseValid = func() bool { return false }

	_, err := service.archiveNextBatch(context.Background(), putter)
	require.NoError(t, err)
	require.Len(t, putter.objects, 1)
	require.EqualValues(t, 3, repo.exportCursor)
}

func TestArchiveSkipsTheTickWhenAnotherReplicaHoldsTheLock(t *testing.T) {
	repo := seededExportRepo()
	repo.lockBusy = true
	putter := &fakePutter{}
	service := enabledService(repo)

	service.runArchive(context.Background(), putter)

	// Another replica is exporting: nothing uploaded, cursor untouched.
	require.Empty(t, putter.objects)
	require.EqualValues(t, 0, repo.exportCursor)
}

func TestArchiveReleasesTheLockAfterTheTick(t *testing.T) {
	repo := seededExportRepo()
	service := enabledService(repo)

	service.runArchive(context.Background(), &fakePutter{})

	require.Equal(t, 1, repo.lockAcquired)
	require.Equal(t, 1, repo.lockReleased)
}

func TestArchiveYieldsWhenAnotherReplicaAdvances(t *testing.T) {
	repo := seededExportRepo()
	repo.casLoses = true
	putter := &fakePutter{}
	service := enabledService(repo)

	exported, err := service.archiveNextBatch(context.Background(), putter)
	require.NoError(t, err)
	// The loser wrote its file (an idempotent overwrite of the winner's),
	// did not advance the cursor, and yields instead of looping.
	require.False(t, exported)
	require.Len(t, putter.objects, 1)
	require.EqualValues(t, 0, repo.exportCursor)
}

func TestArchivePutFailureLeavesTheCursorUntouched(t *testing.T) {
	repo := seededExportRepo()
	putter := &fakePutter{putErr: errors.New("bucket unreachable")}
	service := enabledService(repo)

	_, err := service.archiveNextBatch(context.Background(), putter)
	require.Error(t, err)
	// Nothing advanced: the batch retries at the next tick, nothing is lost.
	require.EqualValues(t, 0, repo.exportCursor)
}

func TestArchiveDeclinesToStartWithoutControlPlane(t *testing.T) {
	service := NewAuditService(nil)
	service.licenseValid = func() bool { return true }
	service.startArchive(context.Background(), time.Minute, &fakePutter{})
	// Declining must not flip the purge into exported-only mode: a stateless
	// service has no exporter to ever advance the cursor.
	require.False(t, service.archiveEnabled)
}

func TestStartArchiveFromEnvStaysOffByDefault(t *testing.T) {
	t.Setenv("ARCHIVE_AUDIT_LOGS", "false")
	service := enabledService(&fakeAuditRepo{})

	require.NoError(t, service.StartArchiveFromEnv(context.Background()))
	require.False(t, service.archiveEnabled)
}

func TestStartArchiveFromEnvRequiresControlPlane(t *testing.T) {
	t.Setenv("ARCHIVE_AUDIT_LOGS", "true")
	service := NewAuditService(nil)
	service.licenseValid = func() bool { return true }

	err := service.StartArchiveFromEnv(context.Background())
	require.ErrorContains(t, err, "control plane")
}

func TestStartArchiveFromEnvSurfacesDestinationErrors(t *testing.T) {
	t.Setenv("ARCHIVE_AUDIT_LOGS", "true")
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_BUCKET_AUDIT_LOGS_NAME", "")
	service := enabledService(&fakeAuditRepo{})

	err := service.StartArchiveFromEnv(context.Background())
	require.ErrorContains(t, err, "S3_BUCKET_AUDIT_LOGS_NAME")
}

func TestStartArchiveFromEnvStartsWithAValidDestination(t *testing.T) {
	t.Setenv("ARCHIVE_AUDIT_LOGS", "true")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_AUDIT_LOGS_BASE_PATH", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := enabledService(&fakeAuditRepo{})

	require.NoError(t, service.StartArchiveFromEnv(ctx))
	// The purge-coordination flag flips synchronously (see PurgeOlderThan).
	require.True(t, service.archiveEnabled)
}
