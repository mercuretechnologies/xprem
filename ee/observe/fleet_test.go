// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"xprem/internal/database"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestBuildFleetFoldsTheTail(t *testing.T) {
	rows := []pgdb.ListObserveFleetFacetsRow{{Dimension: "platform", Value: "ios", Devices: 30}}
	for i := range fleetFacetSize + 2 {
		rows = append(rows, pgdb.ListObserveFleetFacetsRow{
			Dimension: "deviceModel", Value: fmt.Sprintf("model-%d", i), Devices: int64(1000 - i),
		})
	}

	fleet := buildFleet(rows)

	require.Equal(t, uint64(30), fleet.Devices)
	require.Len(t, fleet.Facets, len(fleetDimensions))
	var models FleetFacet
	for _, facet := range fleet.Facets {
		if facet.Dimension == "deviceModel" {
			models = facet
		}
	}
	require.Len(t, models.Values, fleetFacetSize)
	require.Equal(t, "model-0", models.Values[0].Value)
	require.Equal(t, 2, models.OtherValues)
	require.Equal(t, uint64(1000-fleetFacetSize+1000-fleetFacetSize-1), models.Others)
}

func TestBuildFleetKeepsEmbeddedTotalOutsideFacetLimit(t *testing.T) {
	rows := []pgdb.ListObserveFleetFacetsRow{
		{Dimension: "platform", Value: "ios", Devices: int64((fleetFacetSize+1)*10 + 1)},
		{Dimension: "update", Devices: 1},
	}
	for i := range fleetFacetSize + 1 {
		rows = append(rows, pgdb.ListObserveFleetFacetsRow{
			Dimension: "update", Value: fmt.Sprintf("group-%d", i), Context: "group", Devices: 10,
		})
	}

	fleet := buildFleet(rows)
	encoded, err := json.Marshal(fleet)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(encoded, &payload))
	require.Equal(t, float64(1), payload["embeddedDevices"])
	for _, facet := range fleet.Facets {
		if facet.Dimension == "update" {
			require.Len(t, facet.Values, fleetFacetSize)
			for _, value := range facet.Values {
				require.NotEmpty(t, value.Value)
			}
		}
	}
}

func TestReadReleasesCountsDevicesOnWhatTheChannelServes(t *testing.T) {
	_, pgURL := requireLiveStores(t)
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(pgURL)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err)
	defer pool.Close()

	appID := uuid.NewString()
	_, err = pool.Exec(ctx, "INSERT INTO apps (id, name) VALUES ($1, $2)", appID, "releases-"+appID[:8])
	require.NoError(t, err)
	var branchID, runtimeID int64
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO branches (app_id, name) VALUES ($1, 'main') RETURNING id", appID).Scan(&branchID))
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO runtime_versions (app_id, version) VALUES ($1, '1.0.0') RETURNING id", appID).Scan(&runtimeID))
	_, err = pool.Exec(ctx, "INSERT INTO channels (app_id, branch_id, name) VALUES ($1, $2, 'production')", appID, branchID)
	require.NoError(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM channels WHERE app_id = $1", appID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM branches WHERE app_id = $1", appID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM runtime_versions WHERE app_id = $1", appID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM apps WHERE id = $1", appID)
	}()

	publish := func() string {
		updateUUID := uuid.NewString()
		_, err := pool.Exec(ctx, `
			INSERT INTO updates (id, update_uuid, branch_id, runtime_version_id, update_type, commit_hash, platform, checked_at)
			SELECT COALESCE(MAX(id), 0) + 1, $1, $2, $3, 0, 'releases-test', 'ios', CURRENT_TIMESTAMP FROM updates`,
			updateUUID, branchID, runtimeID)
		require.NoError(t, err)
		return updateUUID
	}
	older, newest := publish(), publish()

	addDevice := func(currentUpdate any, lastSeen time.Duration) {
		_, err := pool.Exec(ctx, `
			INSERT INTO device_identity
				(app_id, eas_client_id, last_seen_at, current_update_id, channel_name, runtime_version, platform)
			VALUES ($1, $2, now() - $3::interval, $4, 'production', '1.0.0', 'ios')`,
			appID, uuid.NewString(), fmt.Sprintf("%d seconds", int(lastSeen.Seconds())), currentUpdate)
		require.NoError(t, err)
	}
	addDevice(newest, 0)
	addDevice(older, 0)
	addDevice(nil, 0)
	addDevice(newest, 40*24*time.Hour)

	explorer := NewExplorer(&database.Engine{Queries: pgdb.New(pool), DB: pool}, nil)
	releases, err := explorer.readReleases(ctx, appID, ExplorerQuery{From: time.Now().Add(-30 * 24 * time.Hour)})
	require.NoError(t, err)

	require.Equal(t, []ChannelAdoption{{
		Channel:         "production",
		RuntimeVersion:  "1.0.0",
		ActiveDevices:   3,
		EmbeddedDevices: 1,
		UpToDateDevices: 1,
	}}, releases.Channels)

	readAdoption := func(active, upToDate uint64) {
		t.Helper()
		releases, err := explorer.readReleases(ctx, appID, ExplorerQuery{From: time.Now().Add(-30 * 24 * time.Hour)})
		require.NoError(t, err)
		require.Equal(t, []ChannelAdoption{{
			Channel: "production", RuntimeVersion: "1.0.0", ActiveDevices: active, EmbeddedDevices: 1, UpToDateDevices: upToDate,
		}}, releases.Channels)
	}
	// The control is current only while the update rollout remains active.
	_, err = pool.Exec(ctx, `UPDATE updates SET rollout_percentage = 10,
		control_update_id = (SELECT id FROM updates WHERE update_uuid = $1)
		WHERE update_uuid = $2`, older, newest)
	require.NoError(t, err)
	readAdoption(3, 2)
	_, err = pool.Exec(ctx, "UPDATE updates SET rollout_percentage = NULL WHERE update_uuid = $1", newest)
	require.NoError(t, err)
	readAdoption(3, 1)

	// A rollback serves the embedded bundle, not a nonexistent update UUID.
	var rollbackID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO updates (id, branch_id, runtime_version_id, update_type, commit_hash, platform, checked_at)
		SELECT COALESCE(MAX(id), 0) + 1, $1, $2, 1, 'releases-test', 'ios', CURRENT_TIMESTAMP
		FROM updates RETURNING id`, branchID, runtimeID).Scan(&rollbackID))
	readAdoption(3, 1)

	// An actual rollback control permits embedded devices during an active rollout.
	candidate := publish()
	addDevice(candidate, 0)
	_, err = pool.Exec(ctx, "UPDATE updates SET rollout_percentage = 10, control_update_id = $1 WHERE update_uuid = $2", rollbackID, candidate)
	require.NoError(t, err)
	readAdoption(4, 2)
	_, err = pool.Exec(ctx, "UPDATE updates SET rollout_percentage = NULL WHERE update_uuid = $1", candidate)
	require.NoError(t, err)
	readAdoption(4, 1)
	// A missing control is not a rollback, even while a rollout is active.
	_, err = pool.Exec(ctx, "UPDATE updates SET rollout_percentage = 10, control_update_id = NULL WHERE update_uuid = $1", candidate)
	require.NoError(t, err)
	readAdoption(4, 1)

	// Updates on branches this channel does not serve never count as up to date.
	var unservedBranchID int64
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO branches (app_id, name) VALUES ($1, 'unserved') RETURNING id", appID).Scan(&unservedBranchID))
	unservedUpdate := uuid.NewString()
	_, err = pool.Exec(ctx, `
		INSERT INTO updates (id, update_uuid, branch_id, runtime_version_id, update_type, commit_hash, platform, checked_at)
		SELECT COALESCE(MAX(id), 0) + 1, $1, $2, $3, 0, 'releases-test', 'ios', CURRENT_TIMESTAMP FROM updates`,
		unservedUpdate, unservedBranchID, runtimeID)
	require.NoError(t, err)
	addDevice(unservedUpdate, 0)
	readAdoption(5, 1)
	// The same branch becomes relevant when it is served by the channel rollout.
	_, err = pool.Exec(ctx, `INSERT INTO channel_rollouts (id, channel_id, rollout_branch_id, percentage)
		SELECT $1, id, $2, 10 FROM channels WHERE app_id = $3 AND name = 'production'`, uuid.NewString(), unservedBranchID, appID)
	require.NoError(t, err)
	readAdoption(5, 2)
}
