// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/repository"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestChannelAdoptionDoesNotRescanUpdateHistoryPerDevice(t *testing.T) {
	_, pgURL := requireLiveStores(t)
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(pgURL)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err)
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	// Isolate both the data and its statistics: other integration fixtures must
	// not determine whether PostgreSQL chooses an index or a sequential scan.
	for _, table := range []string{"apps", "branches", "channels", "channel_rollouts", "runtime_versions", "updates", "device_identity"} {
		_, err = tx.Exec(ctx, "CREATE TEMP TABLE "+table+" (LIKE public."+table+" INCLUDING ALL) ON COMMIT DROP")
		require.NoError(t, err)
	}
	appID := uuid.NewString()
	_, err = tx.Exec(ctx, "INSERT INTO apps(id, name) VALUES ($1, 'adoption-performance')", appID)
	require.NoError(t, err)
	var branchID int64
	require.NoError(t, tx.QueryRow(ctx, "INSERT INTO branches(app_id, name) VALUES ($1, 'main') RETURNING id", appID).Scan(&branchID))
	_, err = tx.Exec(ctx, "INSERT INTO channels(app_id, branch_id, name) VALUES ($1, $2, 'production')", appID, branchID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "INSERT INTO runtime_versions(app_id, version) SELECT $1, 'runtime-' || g FROM generate_series(0, 99) g", appID)
	require.NoError(t, err)
	const updates = 2000
	const devices = 1000
	_, err = tx.Exec(ctx, `INSERT INTO updates(id, update_uuid, branch_id, runtime_version_id, update_type, commit_hash, platform, checked_at)
 SELECT row_number() OVER (), gen_random_uuid(), $2, r.id, 0, 'adoption-performance', 'ios', now()
 FROM runtime_versions r CROSS JOIN generate_series(1, 20) g WHERE r.app_id = $1`, appID, branchID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO device_identity(app_id, eas_client_id, last_seen_at, current_update_id, channel_name, runtime_version, platform)
 SELECT $1, gen_random_uuid(), now(), u.update_uuid, 'production', r.version, 'ios'
 FROM generate_series(1, $3::int) g
 JOIN runtime_versions r ON r.app_id = $1 AND r.version = 'runtime-' || (g % 100)
 JOIN LATERAL (
   SELECT update_uuid FROM updates WHERE branch_id = $2 AND runtime_version_id = r.id ORDER BY id DESC LIMIT 1
 ) u ON true`, appID, branchID, devices)
	require.NoError(t, err)
	for _, table := range []string{"apps", "branches", "channels", "channel_rollouts", "runtime_versions", "updates", "device_identity"} {
		_, err = tx.Exec(ctx, "ANALYZE "+table)
		require.NoError(t, err)
	}

	tracked := &adoptionQueryCapture{DBTX: tx}
	rows, err := pgdb.New(tracked).ListObserveChannelAdoption(ctx, pgdb.ListObserveChannelAdoptionParams{
		AppID:       repository.ToPgUUID(appID),
		ActiveSince: pgtype.Timestamptz{Time: time.Now().Add(-24 * time.Hour), Valid: true},
	})
	require.NoError(t, err)
	require.Equal(t, []pgdb.ListObserveChannelAdoptionRow{{ChannelName: "production", ActiveDevices: devices, UpToDateDevices: devices}}, rows)

	// Count the actual update rows visited instead of asserting a duration:
	// the old correlated plan reads two million rows for these 1,000 devices,
	// while a single heads computation reads the history and control table once.
	var planJSON []byte
	require.NoError(t, tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+tracked.sql, tracked.args...).Scan(&planJSON))
	var plans []struct {
		Plan adoptionPlanNode `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal(planJSON, &plans))
	require.Len(t, plans, 1)
	visited := plans[0].Plan.updateRowsVisited()
	require.Greater(t, visited, float64(0))
	require.LessOrEqual(t, visited, float64(updates*2), "update history work must not grow with the device count")
}

type adoptionQueryCapture struct {
	pgdb.DBTX
	sql  string
	args []any
}

func (c *adoptionQueryCapture) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.sql, c.args = sql, args
	return c.DBTX.Query(ctx, sql, args...)
}

type adoptionPlanNode struct {
	RelationName string             `json:"Relation Name"`
	ActualRows   float64            `json:"Actual Rows"`
	ActualLoops  float64            `json:"Actual Loops"`
	Plans        []adoptionPlanNode `json:"Plans"`
}

func (p adoptionPlanNode) updateRowsVisited() float64 {
	var count float64
	if p.RelationName == "updates" {
		count = p.ActualRows * p.ActualLoops
	}
	for _, child := range p.Plans {
		count += child.updateRowsVisited()
	}
	return count
}
