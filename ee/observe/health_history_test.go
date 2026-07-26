// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"os"
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
	chURL := os.Getenv("TEST_CLICKHOUSE_URL")
	pgURL := os.Getenv("TEST_DATABASE_URL")
	if chURL == "" || pgURL == "" {
		t.Skip("TEST_CLICKHOUSE_URL and TEST_DATABASE_URL not both set; skipping outbox delivery test")
	}
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
