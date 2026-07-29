// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"testing"
	"time"

	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres"
	"expo-open-ota/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func newStateFixture(t *testing.T) (*StateHistory, *pgxpool.Pool, string, string) {
	t.Helper()
	_, pgURL := requireLiveStores(t)
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(pgURL)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	appID := uuid.NewString()
	_, err = pool.Exec(ctx, "INSERT INTO apps (id, name) VALUES ($1, $2)", appID, "state-"+appID[:8])
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM apps WHERE id = $1", appID) })

	updateID := uuid.NewString()
	var branchID, runtimeID int64
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO branches (app_id, name) VALUES ($1, $2) RETURNING id", appID, "main").Scan(&branchID))
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO runtime_versions (app_id, version) VALUES ($1, $2) RETURNING id", appID, "1.0.0").Scan(&runtimeID))
	_, err = pool.Exec(ctx, `
		INSERT INTO updates (id, update_uuid, branch_id, runtime_version_id, update_type, commit_hash, platform, checked_at)
		SELECT COALESCE(MAX(id), 0) + 1, $1, $2, $3, 0, 'state-test', 'ios', CURRENT_TIMESTAMP FROM updates`,
		updateID, branchID, runtimeID)
	require.NoError(t, err)

	return NewStateHistory(&database.Engine{Queries: pgdb.New(pool), DB: pool}), pool, appID, updateID
}

func addFailure(t *testing.T, pool *pgxpool.Pool, appID, device, updateID, kind string, from time.Time, to *time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO device_update_failures
			(app_id, eas_client_id, update_id, failure_type, first_seen_at, last_seen_at, resolved_at)
		VALUES ($1, $2, $3, $4, $5, $5, $6)`,
		appID, device, updateID, kind, from, to)
	require.NoError(t, err)
}

// One device holding two faults at once must count as one failing device.
func TestADeviceWithTwoFaultsCountsOnce(t *testing.T) {
	history, pool, appID, updateID := newStateFixture(t)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	device := uuid.NewString()

	addFailure(t, pool, appID, device, updateID, "update_issue", start.Add(10*time.Minute), nil)
	addFailure(t, pool, appID, device, updateID, "runtime_issue", start.Add(20*time.Minute), nil)

	series, err := history.Read(ctx, appID, []string{updateID}, start, start.Add(2*time.Hour))
	require.NoError(t, err)
	for _, point := range series[updateID] {
		require.LessOrEqual(t, point.FailingDevices, uint64(1),
			"two fault rows on one device are one failing device")
	}
	last := series[updateID][len(series[updateID])-1]
	require.Equal(t, uint64(1), last.FailingDevices)
}

// Two non-overlapping faults are two episodes; the healthy stretch between them must read as healthy.
func TestAHealthyStretchBetweenTwoFaultsReadsAsHealthy(t *testing.T) {
	history, pool, appID, updateID := newStateFixture(t)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Hour).Add(-6 * time.Hour)
	device := uuid.NewString()

	firstEnd := start.Add(30 * time.Minute)
	addFailure(t, pool, appID, device, updateID, "update_issue", start.Add(5*time.Minute), &firstEnd)
	addFailure(t, pool, appID, device, updateID, "runtime_issue", start.Add(4*time.Hour), nil)

	series, err := history.Read(ctx, appID, []string{updateID}, start, start.Add(6*time.Hour))
	require.NoError(t, err)

	at := func(offset time.Duration) uint64 {
		want := start.Add(offset)
		var last uint64
		for _, point := range series[updateID] {
			if !point.Timestamp.After(want) {
				last = point.FailingDevices
			}
		}
		return last
	}
	require.Equal(t, uint64(1), at(10*time.Minute), "failing during the first episode")
	require.Equal(t, uint64(0), at(2*time.Hour), "recovered, and the second fault has not happened yet")
	require.Equal(t, uint64(1), at(5*time.Hour), "failing again")
}
