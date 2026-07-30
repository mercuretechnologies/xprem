// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Integration tests for the audit store; they skip unless TEST_DATABASE_URL is set.

package audit

import (
	"context"
	"os"
	"testing"
	"time"
	"xprem/internal/auditlog"

	"xprem/internal/database"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func setupAuditStore(t *testing.T) (*PostgresAuditStore, *pgxpool.Pool) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		// In CI, a skip would silently mean these SQL-only tests never ran.
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI: these tests cover SQL that the in-memory fakes cannot reach")
		}
		t.Skip("TEST_DATABASE_URL not set, start a Postgres and set it to run the audit store tests")
	}
	// The seed migration fails fast on an empty database without the bootstrap pair.
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(dbURL)

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return NewPostgresAuditStore(&database.Engine{Queries: pgdb.New(pool), DB: pool}), pool
}

func insertTestEvent(t *testing.T, auditStore *PostgresAuditStore, event Event) Event {
	t.Helper()
	inserted, err := auditStore.Insert(context.Background(), event)
	require.NoError(t, err)
	return inserted
}

func TestAuditEventRoundtrip(t *testing.T) {
	auditStore, _ := setupAuditStore(t)
	actorID := uuid.NewString()
	appID := uuid.NewString()

	inserted := insertTestEvent(t, auditStore, Event{
		ActorType:     auditlog.ActorUser,
		ActorID:       actorID,
		ActorDisplay:  "axel@example.com",
		Action:        auditlog.ActionAppRenamed,
		TargetType:    "app",
		TargetID:      appID,
		TargetDisplay: "My App",
		AppID:         appID,
		Outcome:       auditlog.OutcomeSuccess,
		IP:            "203.0.113.7",
		UserAgent:     "Mozilla/5.0",
		Metadata: map[string]any{
			"previous_name": "Old App",
			"attempt":       2,
			"forced":        true,
			"context":       map[string]any{"channel": "production"},
		},
	})

	require.Positive(t, inserted.ID)
	// occurred_at is the database's clock, never Go's.
	require.False(t, inserted.OccurredAt.IsZero())
	require.WithinDuration(t, time.Now(), inserted.OccurredAt, time.Minute)

	events, err := auditStore.List(context.Background(), ListParams{
		ListFilters: ListFilters{ActorID: &actorID},
		Limit:       10,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, inserted.ID, events[0].ID)
	require.Equal(t, auditlog.ActorUser, events[0].ActorType)
	require.Equal(t, "axel@example.com", events[0].ActorDisplay)
	require.Equal(t, auditlog.ActionAppRenamed, events[0].Action)
	require.Equal(t, "My App", events[0].TargetDisplay)
	require.Equal(t, appID, events[0].AppID)
	require.Equal(t, "203.0.113.7", events[0].IP)
	// JSON numbers come back as float64: call sites must not expect int.
	require.Equal(t, map[string]any{
		"previous_name": "Old App",
		"attempt":       float64(2),
		"forced":        true,
		"context":       map[string]any{"channel": "production"},
	}, events[0].Metadata)
}

func TestAuditEventAccountLevelAndEmptyMetadata(t *testing.T) {
	auditStore, _ := setupAuditStore(t)
	actorID := uuid.NewString()

	insertTestEvent(t, auditStore, Event{
		ActorType:    auditlog.ActorUser,
		ActorID:      actorID,
		ActorDisplay: "admin@example.com",
		Action:       auditlog.ActionLicenseActivated,
		TargetType:   "license",
		TargetID:     "license",
	})

	events, err := auditStore.List(context.Background(), ListParams{
		ListFilters: ListFilters{ActorID: &actorID},
		Limit:       10,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	// Account-level event: no app, and a nil metadata map (stored as '{}').
	require.Empty(t, events[0].AppID)
	require.Nil(t, events[0].Metadata)
}

func TestAuditEventFilters(t *testing.T) {
	auditStore, _ := setupAuditStore(t)
	actorID := uuid.NewString()
	otherActorID := uuid.NewString()
	appID := uuid.NewString()

	insertTestEvent(t, auditStore, Event{
		ActorType: auditlog.ActorUser, ActorID: actorID, ActorDisplay: "a@example.com",
		Action: auditlog.ActionChannelCreated, TargetType: "channel", TargetID: "staging", AppID: appID,
	})
	insertTestEvent(t, auditStore, Event{
		ActorType: auditlog.ActorUser, ActorID: actorID, ActorDisplay: "a@example.com",
		Action: auditlog.ActionChannelDeleted, TargetType: "channel", TargetID: "staging", AppID: appID,
	})
	insertTestEvent(t, auditStore, Event{
		ActorType: auditlog.ActorAPIKey, ActorID: otherActorID, ActorDisplay: "ci-key",
		Action: auditlog.ActionUpdatePublished, TargetType: "update", TargetID: "1", AppID: appID,
	})

	ctx := context.Background()

	deleted := string(auditlog.ActionChannelDeleted)
	events, err := auditStore.List(ctx, ListParams{
		ListFilters: ListFilters{ActorID: &actorID, Action: &deleted},
		Limit:       10,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, auditlog.ActionChannelDeleted, events[0].Action)

	count, err := auditStore.Count(ctx, ListFilters{AppID: &appID})
	require.NoError(t, err)
	require.EqualValues(t, 3, count)

	future := time.Now().Add(time.Hour)
	count, err = auditStore.Count(ctx, ListFilters{ActorID: &actorID, From: &future})
	require.NoError(t, err)
	require.Zero(t, count)

	past := time.Now().Add(-time.Hour)
	count, err = auditStore.Count(ctx, ListFilters{ActorID: &actorID, From: &past, To: &future})
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
}

func TestAuditEventOutcomeFilter(t *testing.T) {
	auditStore, _ := setupAuditStore(t)
	actorID := uuid.NewString()

	insertTestEvent(t, auditStore, Event{
		ActorType: auditlog.ActorUser, ActorID: actorID, ActorDisplay: "a@example.com",
		Action: auditlog.ActionUserLogin, TargetType: "user", TargetID: actorID,
		Outcome: auditlog.OutcomeSuccess,
	})
	insertTestEvent(t, auditStore, Event{
		ActorType: auditlog.ActorUser, ActorID: actorID, ActorDisplay: "a@example.com",
		Action: auditlog.ActionUserLogin, TargetType: "user", TargetID: actorID,
		Outcome: auditlog.OutcomeFailure,
	})

	failure := string(auditlog.OutcomeFailure)
	events, err := auditStore.List(context.Background(), ListParams{
		ListFilters: ListFilters{ActorID: &actorID, Outcome: &failure},
		Limit:       10,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, auditlog.OutcomeFailure, events[0].Outcome)
}

func TestPurgeBeforeRemovesOnlyExpiredRows(t *testing.T) {
	auditStore, pool := setupAuditStore(t)
	actorID := uuid.NewString()
	ctx := context.Background()

	expired := insertTestEvent(t, auditStore, Event{
		ActorType: auditlog.ActorUser, ActorID: actorID, ActorDisplay: "a@example.com",
		Action: auditlog.ActionUserLogin, TargetType: "user", TargetID: actorID,
	})
	fresh := insertTestEvent(t, auditStore, Event{
		ActorType: auditlog.ActorUser, ActorID: actorID, ActorDisplay: "a@example.com",
		Action: auditlog.ActionUserLogin, TargetType: "user", TargetID: actorID,
	})
	// occurred_at is the database's clock, so the expired row is aged by hand.
	_, err := pool.Exec(ctx,
		"UPDATE audit_log_events SET occurred_at = now() - interval '600 days' WHERE id = $1",
		expired.ID)
	require.NoError(t, err)

	purged, err := auditStore.PurgeBefore(ctx, time.Now().Add(-550*24*time.Hour), false)
	require.NoError(t, err)
	require.GreaterOrEqual(t, purged, int64(1))

	events, err := auditStore.List(ctx, ListParams{
		ListFilters: ListFilters{ActorID: &actorID},
		Limit:       10,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, fresh.ID, events[0].ID)
}

func TestExportCursorAndListAfter(t *testing.T) {
	auditStore, pool := setupAuditStore(t)
	ctx := context.Background()
	actorID := uuid.NewString()

	first := insertTestEvent(t, auditStore, Event{
		ActorType: auditlog.ActorUser, ActorID: actorID, ActorDisplay: "a@example.com",
		Action: auditlog.ActionUserLogin, TargetType: "user", TargetID: actorID,
	})
	second := insertTestEvent(t, auditStore, Event{
		ActorType: auditlog.ActorUser, ActorID: actorID, ActorDisplay: "a@example.com",
		Action: auditlog.ActionUserLogin, TargetType: "user", TargetID: actorID,
	})
	// ListAfter hides rows younger than its visibility lag, so these are aged past it by hand.
	_, err := pool.Exec(ctx,
		"UPDATE audit_log_events SET occurred_at = now() - interval '1 minute' WHERE id = ANY($1)",
		[]int64{first.ID, second.ID})
	require.NoError(t, err)

	// The shared table holds other tests' rows too, so assert order and membership, not exact contents.
	events, err := auditStore.ListAfter(ctx, first.ID-1, 100000)
	require.NoError(t, err)
	indexOf := func(id int64) int {
		for i, event := range events {
			if event.ID == id {
				return i
			}
		}
		return -1
	}
	require.GreaterOrEqual(t, indexOf(first.ID), 0)
	require.Greater(t, indexOf(second.ID), indexOf(first.ID))
	for i := 1; i < len(events); i++ {
		require.Greater(t, events[i].ID, events[i-1].ID)
	}
	// Strictly after: the cursor row itself is excluded.
	afterSecond, err := auditStore.ListAfter(ctx, second.ID, 100000)
	require.NoError(t, err)
	require.Equal(t, -1, func() int {
		for i, event := range afterSecond {
			if event.ID == second.ID {
				return i
			}
		}
		return -1
	}())

	// A row younger than 30 seconds stays invisible to the exporter.
	fresh := insertTestEvent(t, auditStore, Event{
		ActorType: auditlog.ActorUser, ActorID: actorID, ActorDisplay: "a@example.com",
		Action: auditlog.ActionUserLogin, TargetType: "user", TargetID: actorID,
	})
	lagged, err := auditStore.ListAfter(ctx, first.ID-1, 100000)
	require.NoError(t, err)
	for _, event := range lagged {
		require.NotEqual(t, fresh.ID, event.ID)
	}

	// Re-advancing to the same value keeps shared state intact for other test runs.
	cursor, err := auditStore.ExportCursor(ctx)
	require.NoError(t, err)
	advanced, err := auditStore.AdvanceExportCursor(ctx, cursor, cursor)
	require.NoError(t, err)
	require.True(t, advanced)
	advanced, err = auditStore.AdvanceExportCursor(ctx, cursor+987654321, cursor)
	require.NoError(t, err)
	require.False(t, advanced)
}

func TestExportLockIsExclusive(t *testing.T) {
	auditStore, _ := setupAuditStore(t)
	ctx := context.Background()

	release, locked, err := auditStore.TryExportLock(ctx)
	require.NoError(t, err)
	require.True(t, locked)

	// A second claim, like another replica, must lose while the first holds the lock.
	_, lockedAgain, err := auditStore.TryExportLock(ctx)
	require.NoError(t, err)
	require.False(t, lockedAgain)

	release()
	releaseAfter, lockedAfter, err := auditStore.TryExportLock(ctx)
	require.NoError(t, err)
	require.True(t, lockedAfter)
	releaseAfter()
}

func TestPurgeExportedOnlySparesUnarchivedRows(t *testing.T) {
	auditStore, pool := setupAuditStore(t)
	actorID := uuid.NewString()
	ctx := context.Background()

	archived := insertTestEvent(t, auditStore, Event{
		ActorType: auditlog.ActorUser, ActorID: actorID, ActorDisplay: "a@example.com",
		Action: auditlog.ActionUserLogin, TargetType: "user", TargetID: actorID,
	})
	unarchived := insertTestEvent(t, auditStore, Event{
		ActorType: auditlog.ActorUser, ActorID: actorID, ActorDisplay: "a@example.com",
		Action: auditlog.ActionUserLogin, TargetType: "user", TargetID: actorID,
	})
	// Both are past retention; only the first is behind the export cursor.
	_, err := pool.Exec(ctx,
		"UPDATE audit_log_events SET occurred_at = now() - interval '600 days' WHERE id = ANY($1)",
		[]int64{archived.ID, unarchived.ID})
	require.NoError(t, err)
	cursor, err := auditStore.ExportCursor(ctx)
	require.NoError(t, err)
	advanced, err := auditStore.AdvanceExportCursor(ctx, cursor, archived.ID)
	require.NoError(t, err)
	require.True(t, advanced)

	_, err = auditStore.PurgeBefore(ctx, time.Now().Add(-550*24*time.Hour), true)
	require.NoError(t, err)

	events, err := auditStore.List(ctx, ListParams{
		ListFilters: ListFilters{ActorID: &actorID},
		Limit:       10,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	// The archived row is gone; the expired-but-unarchived one survives.
	require.Equal(t, unarchived.ID, events[0].ID)
}

func TestAuditEventKeysetPagination(t *testing.T) {
	auditStore, _ := setupAuditStore(t)
	actorID := uuid.NewString()

	var insertedIDs []int64
	for range 5 {
		inserted := insertTestEvent(t, auditStore, Event{
			ActorType: auditlog.ActorUser, ActorID: actorID, ActorDisplay: "a@example.com",
			Action: auditlog.ActionUserLogin, TargetType: "user", TargetID: actorID,
		})
		insertedIDs = append(insertedIDs, inserted.ID)
	}

	ctx := context.Background()
	filters := ListFilters{ActorID: &actorID}

	page1, err := auditStore.List(ctx, ListParams{ListFilters: filters, Limit: 2})
	require.NoError(t, err)
	require.Len(t, page1, 2)
	require.Equal(t, insertedIDs[4], page1[0].ID)
	require.Equal(t, insertedIDs[3], page1[1].ID)

	// Second page starts strictly below the cursor: no overlap, no gap.
	cursor := page1[1].ID
	page2, err := auditStore.List(ctx, ListParams{ListFilters: filters, BeforeID: &cursor, Limit: 2})
	require.NoError(t, err)
	require.Len(t, page2, 2)
	require.Equal(t, insertedIDs[2], page2[0].ID)
	require.Equal(t, insertedIDs[1], page2[1].ID)

	cursor = page2[1].ID
	page3, err := auditStore.List(ctx, ListParams{ListFilters: filters, BeforeID: &cursor, Limit: 2})
	require.NoError(t, err)
	require.Len(t, page3, 1)
	require.Equal(t, insertedIDs[0], page3[0].ID)
}
