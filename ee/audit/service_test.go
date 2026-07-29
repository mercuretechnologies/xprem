// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package audit

import (
	"context"
	"errors"
	"testing"
	"time"
	"xprem/internal/auditlog"

	"github.com/stretchr/testify/require"
)

type fakeAuditRepo struct {
	inserted []Event
	// ctxErrAtInsert and hadDeadline capture the state of the context Record
	// hands to the repository, to prove the insert survives request
	// cancellation while staying time-bounded.
	ctxErrAtInsert    error
	hadDeadline       bool
	insertErr         error
	listErr           error
	listResult        []Event
	listParams        ListParams
	purgeCutoff       time.Time
	purgeExportedOnly bool
	purgedCount       int64
	exportCursor      int64
	// casLoses makes AdvanceExportCursor lose the optimistic race, like a
	// concurrent replica advancing first.
	casLoses bool
	// lockBusy simulates another replica holding the export advisory lock.
	lockBusy     bool
	lockAcquired int
	lockReleased int
}

func (f *fakeAuditRepo) Insert(ctx context.Context, event Event) (Event, error) {
	f.ctxErrAtInsert = ctx.Err()
	_, f.hadDeadline = ctx.Deadline()
	if f.insertErr != nil {
		return Event{}, f.insertErr
	}
	event.ID = int64(len(f.inserted) + 1)
	f.inserted = append(f.inserted, event)
	return event, nil
}

// List honors BeforeID and Limit like the real store (listResult must be
// seeded newest first), so the handler tests can walk real pages.
func (f *fakeAuditRepo) List(ctx context.Context, params ListParams) ([]Event, error) {
	f.listParams = params
	if f.listErr != nil {
		return nil, f.listErr
	}
	result := make([]Event, 0, len(f.listResult))
	for _, event := range f.listResult {
		if params.BeforeID != nil && event.ID >= *params.BeforeID {
			continue
		}
		result = append(result, event)
	}
	if params.Limit < len(result) {
		result = result[:params.Limit]
	}
	return result, nil
}

func (f *fakeAuditRepo) Count(ctx context.Context, filters ListFilters) (int64, error) {
	return int64(len(f.listResult)), nil
}

func (f *fakeAuditRepo) PurgeBefore(ctx context.Context, cutoff time.Time, exportedOnly bool) (int64, error) {
	f.purgeCutoff = cutoff
	f.purgeExportedOnly = exportedOnly
	return f.purgedCount, nil
}

// ListAfter mirrors the store: strictly after the cursor, oldest first
// (listResult is seeded newest first, so it scans backwards).
func (f *fakeAuditRepo) ListAfter(ctx context.Context, afterID int64, limit int) ([]Event, error) {
	result := make([]Event, 0, limit)
	for i := len(f.listResult) - 1; i >= 0; i-- {
		event := f.listResult[i]
		if event.ID <= afterID {
			continue
		}
		result = append(result, event)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (f *fakeAuditRepo) TryExportLock(ctx context.Context) (func(), bool, error) {
	if f.lockBusy {
		return nil, false, nil
	}
	f.lockAcquired++
	return func() { f.lockReleased++ }, true, nil
}

func (f *fakeAuditRepo) ExportCursor(ctx context.Context) (int64, error) {
	return f.exportCursor, nil
}

func (f *fakeAuditRepo) AdvanceExportCursor(ctx context.Context, from int64, to int64) (bool, error) {
	if f.casLoses || f.exportCursor != from {
		return false, nil
	}
	f.exportCursor = to
	return true, nil
}

func enabledService(repo AuditRepository) *AuditService {
	svc := NewAuditService(repo)
	svc.licenseValid = func() bool { return true }
	return svc
}

func TestRecordCollectsNothingWithoutLicense(t *testing.T) {
	repo := &fakeAuditRepo{}
	service := NewAuditService(repo)
	service.licenseValid = func() bool { return false }

	service.Record(context.Background(), Event{Action: auditlog.ActionUserLogin})

	require.Empty(t, repo.inserted, "an unlicensed deployment must not collect a single event")
	require.False(t, service.Enabled())
}

func TestRecordCollectsNothingInStatelessMode(t *testing.T) {
	service := NewAuditService(nil)
	service.licenseValid = func() bool { return true }

	// Must be a silent no-op, not a nil dereference.
	service.Record(context.Background(), Event{Action: auditlog.ActionUserLogin})
	require.False(t, service.Enabled())
}

func TestRecordDoesNotFabricateMissingFields(t *testing.T) {
	repo := &fakeAuditRepo{}
	service := enabledService(repo)

	service.Record(context.Background(), Event{
		Action:     auditlog.ActionUserSSOProvisioned,
		TargetType: "user",
		TargetID:   "u-1",
	})

	require.Len(t, repo.inserted, 1)
	// An incomplete call site must show as '' (unknown) in the log, not be
	// papered over with "system"/"success".
	require.Empty(t, repo.inserted[0].ActorType)
	require.Empty(t, repo.inserted[0].Outcome)
}

func TestRecordSurvivesRequestCancellation(t *testing.T) {
	repo := &fakeAuditRepo{}
	service := enabledService(repo)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service.Record(ctx, Event{Action: auditlog.ActionUserLogin})

	require.Len(t, repo.inserted, 1)
	require.NoError(t, repo.ctxErrAtInsert, "the insert must not inherit the request's cancellation")
	require.True(t, repo.hadDeadline, "the insert must stay time-bounded so a hung database cannot pile up handlers")
}

func TestRecordSwallowsInsertErrors(t *testing.T) {
	repo := &fakeAuditRepo{insertErr: errors.New("database down")}
	service := enabledService(repo)

	// Best-effort contract: the mutation that emitted the event must not fail.
	service.Record(context.Background(), Event{Action: auditlog.ActionUserLogin})
	require.Empty(t, repo.inserted)
}

func TestListRequiresControlPlane(t *testing.T) {
	service := NewAuditService(nil)
	service.licenseValid = func() bool { return true }

	_, _, err := service.List(context.Background(), ListParams{})
	require.ErrorIs(t, err, ErrRequiresControlPlane)

	_, err = service.Count(context.Background(), ListFilters{})
	require.ErrorIs(t, err, ErrRequiresControlPlane)
}

func TestListClampsPageSize(t *testing.T) {
	repo := &fakeAuditRepo{}
	service := enabledService(repo)

	_, _, err := service.List(context.Background(), ListParams{})
	require.NoError(t, err)
	// The service asks for one extra row to detect the next page.
	require.Equal(t, DefaultPageSize+1, repo.listParams.Limit)

	_, _, err = service.List(context.Background(), ListParams{Limit: MaxPageSize + 50})
	require.NoError(t, err)
	require.Equal(t, MaxPageSize+1, repo.listParams.Limit)
}

func TestListPagination(t *testing.T) {
	events := make([]Event, 0, 3)
	for i := 3; i >= 1; i-- {
		events = append(events, Event{ID: int64(i), Action: auditlog.ActionUserLogin})
	}
	repo := &fakeAuditRepo{listResult: events}
	service := enabledService(repo)

	// Three rows available, page size two: a full page and a cursor at its
	// last row.
	page, nextCursor, err := service.List(context.Background(), ListParams{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page, 2)
	require.NotNil(t, nextCursor)
	require.Equal(t, int64(2), *nextCursor)

	// Three rows available, page size three: last page, no cursor.
	page, nextCursor, err = service.List(context.Background(), ListParams{Limit: 3})
	require.NoError(t, err)
	require.Len(t, page, 3)
	require.Nil(t, nextCursor)
}

func TestPurgeOlderThanUsesTheRetentionCutoff(t *testing.T) {
	repo := &fakeAuditRepo{purgedCount: 12}
	// Deliberately unlicensed: retention applies to collected data whatever
	// the licence state.
	service := NewAuditService(repo)
	service.licenseValid = func() bool { return false }

	purged, err := service.PurgeOlderThan(context.Background(), 550*24*time.Hour)
	require.NoError(t, err)
	require.EqualValues(t, 12, purged)
	require.WithinDuration(t, time.Now().Add(-550*24*time.Hour), repo.purgeCutoff, time.Minute)
	// No archive: the purge deletes by age alone.
	require.False(t, repo.purgeExportedOnly)
}

func TestPurgeSparesUnarchivedRowsWhileArchiving(t *testing.T) {
	repo := &fakeAuditRepo{}
	service := NewAuditService(repo)
	service.licenseValid = func() bool { return true }
	service.startArchive(context.Background(), time.Hour, &fakePutter{})

	_, err := service.PurgeOlderThan(context.Background(), 550*24*time.Hour)
	require.NoError(t, err)
	// Archiving on: expired rows the exporter has not reached yet must
	// survive, so the purge switches to the exported-only variant.
	require.True(t, repo.purgeExportedOnly)
}

func TestPurgeRequiresControlPlane(t *testing.T) {
	service := NewAuditService(nil)
	service.licenseValid = func() bool { return true }
	_, err := service.PurgeOlderThan(context.Background(), time.Hour)
	require.ErrorIs(t, err, ErrRequiresControlPlane)
	// And the scheduler declines to start rather than panic.
	service.startRetentionPurge(context.Background(), time.Hour)
}

func TestListReadsStayOpenWithoutLicense(t *testing.T) {
	repo := &fakeAuditRepo{listResult: []Event{{ID: 1, Action: auditlog.ActionUserLogin, OccurredAt: time.Now()}}}
	service := NewAuditService(repo)
	service.licenseValid = func() bool { return false }

	// A lapsed license stops collection, never read access to what was
	// collected while licensed.
	page, _, err := service.List(context.Background(), ListParams{})
	require.NoError(t, err)
	require.Len(t, page, 1)
}
