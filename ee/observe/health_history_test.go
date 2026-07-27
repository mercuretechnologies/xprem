// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"testing"
	"time"

	"expo-open-ota/ee/identity"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/clickhouse"
	"expo-open-ota/internal/database/postgres"
	"expo-open-ota/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type recordingSnapshotBatch struct {
	values []any
}

func (b *recordingSnapshotBatch) Append(values ...any) error {
	b.values = values
	return nil
}

func TestAppendSnapshotMapsAndClampsDatabaseValues(t *testing.T) {
	appID, updateID := uuid.New(), uuid.New()
	bucket := time.Date(2026, 7, 24, 10, 30, 0, 0, time.UTC)
	capturedAt := bucket.Add(12 * time.Second)
	batch := &recordingSnapshotBatch{}

	err := appendSnapshot(batch, pgdb.ListCurrentUpdateHealthSnapshotsRow{
		AppID:             pgtype.UUID{Bytes: appID, Valid: true},
		UpdateUuid:        pgtype.UUID{Bytes: updateID, Valid: true},
		Role:              "candidate",
		DevicesOnUpdate:   12,
		SuccessfulDevices: 10,
		FaultyDevices:     2,
		UpdateIssues:      -1,
		RuntimeIssues:     2,
	}, bucket, capturedAt)

	require.NoError(t, err)
	require.Equal(t, []any{
		appID.String(),
		updateID.String(),
		bucket,
		capturedAt,
		"candidate",
		uint64(12),
		uint64(10),
		uint64(2),
		uint64(0),
		uint64(2),
	}, batch.values)
}

// The outbox is where an event stops being a pair of ids and becomes something
// a segmented chart can group by. The dimensions are resolved once, at
// delivery, and frozen on the row: that is what stops a chart from relabelling
// a device's whole history the day it upgrades. This walks the real path,
// trigger included, because the trigger and the resolving query are the two
// halves that have to agree.
func TestOutboxDeliveryFreezesEventDimensions(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	// The seed migration refuses without these and RunDBMigrations reports it
	// with log.Fatalf, which takes the whole package binary down, unit tests
	// included. Every other store test in the repo sets them for that reason.
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(pgURL)
	clickhouse.RunDBMigrations(chURL, pgURL)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err)
	defer pool.Close()
	chEngine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer chEngine.Close()

	appID := uuid.NewString()
	_, err = pool.Exec(ctx, "INSERT INTO apps (id, name) VALUES ($1, $2)", appID, "outbox-"+appID[:8])
	require.NoError(t, err)
	defer func() { _, _ = pool.Exec(context.Background(), "DELETE FROM apps WHERE id = $1", appID) }()

	var branchID, runtimeVersionID int64
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO branches (app_id, name) VALUES ($1, $2) RETURNING id", appID, "production").Scan(&branchID))
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO runtime_versions (app_id, version) VALUES ($1, $2) RETURNING id", appID, "1.0.0").Scan(&runtimeVersionID))
	updateID := uuid.NewString()
	// updates.id is assigned by the publish path, not by a sequence.
	_, err = pool.Exec(ctx, `
		INSERT INTO updates (id, update_uuid, branch_id, runtime_version_id, update_type, commit_hash, platform, checked_at)
		SELECT COALESCE(MAX(id), 0) + 1, $1, $2, $3, 0, 'outbox-test', 'ios', CURRENT_TIMESTAMP FROM updates`,
		updateID, branchID, runtimeVersionID)
	require.NoError(t, err)

	// A telemetry check-in: the only source that knows the hardware and the
	// store version, and the one the manifest path cannot replace.
	store := identity.NewPostgresIdentityStore(&database.Engine{Queries: pgdb.New(pool), DB: pool})
	deviceID := uuid.NewString()
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, &identity.CurrentUpdate{ID: updateID, ObservedAt: time.Now().UTC()}, identity.DeviceInfo{
		Model: "iPhone18,2", OSName: "iOS", OSVersion: "26.1", AppVersion: "1.4.0",
	}))

	history := NewHealthHistory(&database.Engine{Queries: pgdb.New(pool), DB: pool}, chEngine)
	delivered, err := history.deliverOutboxBatch(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, delivered, "registering a device on an update enqueues exactly one first_seen")

	var branch, runtimeVersion, platform, osName, osVersion, deviceModel, appVersion string
	require.NoError(t, chEngine.Conn.QueryRow(ctx, `
		SELECT branch, runtime_version, platform, os_name, os_version, device_model, app_version
		FROM device_health_events WHERE app_id = ? AND eas_client_id = ?`, appID, deviceID,
	).Scan(&branch, &runtimeVersion, &platform, &osName, &osVersion, &deviceModel, &appVersion))

	// The update side is permanent, so it is exact by construction.
	require.Equal(t, "production", branch)
	require.Equal(t, "1.0.0", runtimeVersion)
	require.Equal(t, "ios", platform)
	// The device side is what the registry knew when the event was delivered.
	require.Equal(t, "iOS", osName)
	require.Equal(t, "26.1", osVersion)
	require.Equal(t, "iPhone18,2", deviceModel)
	require.Equal(t, "1.4.0", appVersion)

	// A store release later must not reach back into the event already
	// written: that row is what the device was then, and rewriting it is the
	// bug this whole column set exists to avoid.
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, nil, identity.DeviceInfo{AppVersion: "2.0.0"}))
	var frozen string
	require.NoError(t, chEngine.Conn.QueryRow(ctx,
		"SELECT app_version FROM device_health_events WHERE app_id = ? AND eas_client_id = ?", appID, deviceID,
	).Scan(&frozen))
	require.Equal(t, "1.4.0", frozen)
}

// The outbox delivers at-least-once by design: the ClickHouse insert no longer
// runs inside the PostgreSQL transaction, so a crash between the send and the
// delete replays the batch. That is only safe because every read of
// device_health_events tolerates a repeated row, and this pins that rather than
// trusting the reading of three queries.
//
// device_health_events is a ReplacingMergeTree keyed on (app_id, outbox_id),
// but a ReplacingMergeTree only collapses at merge time, so the reads cannot
// wait for it: the log feed groups by outbox_id, and the segmented health read
// counts through a DISTINCT population and an ASOF join that matches one row
// per device and bucket whatever the source holds.
func TestReplayedOutboxEventIsNotCountedTwice(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(pgURL)
	clickhouse.RunDBMigrations(chURL, pgURL)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err)
	defer pool.Close()
	chEngine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer chEngine.Close()

	appID := uuid.NewString()
	_, err = pool.Exec(ctx, "INSERT INTO apps (id, name) VALUES ($1, $2)", appID, "replay-"+appID[:8])
	require.NoError(t, err)
	defer func() { _, _ = pool.Exec(context.Background(), "DELETE FROM apps WHERE id = $1", appID) }()

	updateID, deviceID := uuid.NewString(), uuid.NewString()
	adopted := time.Now().UTC().Add(-10 * time.Minute)

	// One adoption, written twice under the SAME outbox_id: exactly what a
	// replay after a crash between the send and the delete produces.
	insert := func() {
		batch, err := chEngine.Conn.PrepareBatch(ctx, `INSERT INTO device_health_events
			(outbox_id, event_type, app_id, eas_client_id, update_id, previous_update_id,
			 failure_type, fatal_error, occurred_at,
			 branch, runtime_version, platform, os_name, os_version, device_model, country_code,
			 app_version)`)
		require.NoError(t, err)
		require.NoError(t, batch.Append(
			uint64(4242), "first_seen", appID, deviceID, updateID, nil,
			"", "", adopted,
			"production", "1.0.0", "ios", "iOS", "26.1", "iPhone18,2", "FR", "1.4.0",
		))
		require.NoError(t, batch.Send())
	}
	insert()
	insert()

	var raw uint64
	require.NoError(t, chEngine.Conn.QueryRow(ctx,
		"SELECT count() FROM device_health_events WHERE app_id = ? AND outbox_id = 4242", appID,
	).Scan(&raw))
	require.Equal(t, uint64(2), raw, "fixture: the duplicate must really be there, unmerged")

	// And the read that matters still sees one device.
	history := NewHealthHistory(&database.Engine{Queries: pgdb.New(pool), DB: pool}, chEngine)
	segments, err := history.ReadBySegment(
		ctx, appID, []string{updateID}, "country",
		adopted.Add(-time.Minute), time.Now().UTC(),
	)
	require.NoError(t, err)
	require.NotEmpty(t, segments["FR"], "the adoption must be visible")
	for _, point := range segments["FR"] {
		require.LessOrEqual(t, point.DevicesOnUpdate, uint64(1),
			"a replayed event must not turn one device into two")
	}
}

// Only one replica drains at a time, and that is now a session advisory lock
// rather than row locks held across the ClickHouse insert. A second drainer
// finding the lock taken reports nothing delivered instead of delivering the
// same rows again.
func TestOutboxDrainIsSerializedAcrossReplicas(t *testing.T) {
	_, pgURL := requireLiveStores(t)
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(pgURL)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err)
	defer pool.Close()

	first := NewHealthHistory(&database.Engine{Queries: pgdb.New(pool), DB: pool}, nil)
	release, locked, err := first.lockOutbox(ctx)
	require.NoError(t, err)
	require.True(t, locked, "the first drainer takes the lock")

	// A second process, its own pool, the way a second replica would come.
	otherPool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err)
	defer otherPool.Close()
	second := NewHealthHistory(&database.Engine{Queries: pgdb.New(otherPool), DB: otherPool}, nil)

	_, alsoLocked, err := second.lockOutbox(ctx)
	require.NoError(t, err)
	require.False(t, alsoLocked, "a second drainer must stand down while the first holds the lock")

	// And the lock is released, not leaked: once the first is done the second
	// can take over.
	release()
	thirdRelease, freeNow, err := second.lockOutbox(ctx)
	require.NoError(t, err)
	require.True(t, freeNow, "the lock must be released when the drain ends")
	thirdRelease()
}

// The fleet snapshot is elected too, on its own lock. It aggregates the whole
// device_identity table and then joins the failures of every tracked update,
// and every replica used to run it on its own timer.
//
// Its own lock rather than the outbox's: they run on different cadences, one
// every second and one every minute, and sharing would have let a slow drain
// starve a capture.
func TestSnapshotCaptureIsElectedSeparatelyFromTheDrain(t *testing.T) {
	_, pgURL := requireLiveStores(t)
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(pgURL)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err)
	defer pool.Close()
	otherPool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err)
	defer otherPool.Close()

	releaseSnapshot, locked, err := postgres.TryAdvisoryLock(ctx, pool, snapshotAdvisoryLockID, "health snapshot")
	require.NoError(t, err)
	require.True(t, locked)

	_, second, err := postgres.TryAdvisoryLock(ctx, otherPool, snapshotAdvisoryLockID, "health snapshot")
	require.NoError(t, err)
	require.False(t, second, "a second replica must not capture the same minute")

	// The drain is a different lock, so holding the snapshot one leaves it free.
	releaseDrain, drainFree, err := postgres.TryAdvisoryLock(ctx, otherPool, outboxAdvisoryLockID, "health outbox")
	require.NoError(t, err)
	require.True(t, drainFree, "the drain must not wait on the capture")
	releaseDrain()

	releaseSnapshot()
	// Released, not just checked: a held lock pins a connection out of the
	// pool, and pgxpool.Close waits for every acquired connection to come
	// back, so forgetting it hangs the test rather than failing it.
	releaseAgain, freeNow, err := postgres.TryAdvisoryLock(ctx, otherPool, snapshotAdvisoryLockID, "health snapshot")
	require.NoError(t, err)
	require.True(t, freeNow, "the lock must be released when the capture ends")
	releaseAgain()
}
