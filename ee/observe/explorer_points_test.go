// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"fmt"
	"testing"
	"time"

	"xprem/internal/database"
	"xprem/internal/database/clickhouse"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// TestReadMetricPointsAsksOnlyForTheKeptSeries verifies readMetricPoints only
// returns the series the caller asked to keep.
func TestReadMetricPointsAsksOnlyForTheKeptSeries(t *testing.T) {
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
	now := time.Now().UTC().Truncate(time.Minute)
	sink := NewClickHouseTelemetrySink(chEngine)
	row := func(name string, value float64) MetricRow {
		return MetricRow{
			Envelope: Envelope{
				AppID:         appID,
				EASClientID:   uuid.NewString(),
				SessionID:     uuid.NewString(),
				UpdateID:      ZeroUpdateID,
				UpdateGroupID: ZeroUpdateID,
				Timestamp:     now.Add(-time.Minute),
				ContentKey:    contentKey(name, fmt.Sprint(value)),
			},
			MetricName: name,
			Value:      value,
		}
	}
	require.NoError(t, sink.InsertMetrics(ctx, []MetricRow{
		row("expo.app_startup.cold_launch_time", 1.5),
		row("expo.navigation.cold_ttr", 9.5),
	}))

	explorer := NewExplorer(&database.Engine{Queries: pgdb.New(pool), DB: pool}, chEngine)
	points, err := explorer.readMetricPoints(ctx, appID, ExplorerQuery{
		From:   now.Add(-time.Hour),
		To:     now.Add(time.Minute),
		Bucket: time.Minute,
	}, false, []string{"expo.app_startup.cold_launch_time"})
	require.NoError(t, err)

	require.Len(t, points, 1)
	require.Contains(t, points, "expo.app_startup.cold_launch_time")
	require.Len(t, points["expo.app_startup.cold_launch_time"], 1)
	require.InDelta(t, 1.5, points["expo.app_startup.cold_launch_time"][0].Value, 0.0001)
	require.NotContains(t, points, "expo.navigation.cold_ttr")

	empty, err := explorer.readMetricPoints(ctx, appID, ExplorerQuery{
		From: now.Add(-time.Hour), To: now.Add(time.Minute), Bucket: time.Minute,
	}, false, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}
