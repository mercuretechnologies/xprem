// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"bytes"
	"context"
	"testing"
	"time"
	"xprem/internal/database/clickhouse"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Needs TEST_CLICKHOUSE_URL and TEST_DATABASE_URL to run.
func TestClickHouseTelemetrySinkRoundTrip(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)

	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()
	sink := NewClickHouseTelemetrySink(engine)

	appID := uuid.NewString()
	now := time.Now().UTC()

	metricBatch, err := DecodeMetrics(bytes.NewReader(loadFixture(t, "ios_metrics.json")))
	require.NoError(t, err)
	metricRows := FlattenMetrics(appID, metricBatch, now)
	require.NotEmpty(t, metricRows)
	for i := range metricRows {
		metricRows[i].Branch = "main"
	}
	require.NoError(t, sink.InsertMetrics(ctx, metricRows))

	logBatch, err := DecodeLogs(bytes.NewReader(loadFixture(t, "ios_logs.json")))
	require.NoError(t, err)
	logRows := FlattenLogs(appID, logBatch, now)
	require.NotEmpty(t, logRows)
	require.NoError(t, sink.InsertLogs(ctx, logRows))

	var metricCount, logCount uint64
	require.NoError(t, engine.Conn.QueryRow(ctx,
		"SELECT count() FROM observe_metrics WHERE app_id = ?", appID).Scan(&metricCount))
	require.EqualValues(t, len(metricRows), metricCount)
	require.NoError(t, engine.Conn.QueryRow(ctx,
		"SELECT count() FROM observe_logs WHERE app_id = ?", appID).Scan(&logCount))
	require.EqualValues(t, len(logRows), logCount)

	var branch, updateID, platform string
	var value float64
	require.NoError(t, engine.Conn.QueryRow(ctx, `
		SELECT branch, toString(update_id), platform, value
		FROM observe_metrics
		WHERE app_id = ? AND metric_name = 'expo.app_startup.tti'`, appID,
	).Scan(&branch, &updateID, &platform, &value))
	assert.Equal(t, "main", branch)
	assert.Equal(t, "9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10", updateID)
	assert.Equal(t, "ios", platform)
	assert.InDelta(t, 1.842, value, 0.0001)

	var fatalCount uint64
	require.NoError(t, engine.Conn.QueryRow(ctx, `
		SELECT count() FROM observe_logs
		WHERE app_id = ? AND event_name = 'exception' AND is_fatal = 1`, appID).Scan(&fatalCount))
	require.EqualValues(t, 1, fatalCount)

	require.NoError(t, sink.InsertLogs(ctx, logRows))
	var total, distinct uint64
	require.NoError(t, engine.Conn.QueryRow(ctx, `
		SELECT count(), uniqExact(content_key) FROM observe_logs WHERE app_id = ?`, appID,
	).Scan(&total, &distinct))
	assert.EqualValues(t, 2*len(logRows), total)
	assert.EqualValues(t, len(logRows), distinct)
}

func TestHealthHistoryRoundTripUsesLatestSnapshotInMinute(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)

	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()

	appID, updateID, emptyUpdateID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	bucket := time.Now().UTC().Truncate(time.Minute)
	batch, err := engine.Conn.PrepareBatch(ctx, `INSERT INTO update_health_snapshots
		(app_id, update_id, bucket, captured_at, role, devices_on_update,
		 successful_devices, faulty_devices, update_issues, runtime_issues)`)
	require.NoError(t, err)
	require.NoError(t, batch.Append(
		appID, updateID, bucket, bucket.Add(time.Second), "candidate",
		uint64(10), uint64(9), uint64(1), uint64(1), uint64(0),
	))
	require.NoError(t, batch.Append(
		appID, updateID, bucket, bucket.Add(2*time.Second), "candidate",
		uint64(20), uint64(18), uint64(2), uint64(1), uint64(1),
	))
	require.NoError(t, batch.Send())

	history := NewHealthHistory(nil, engine)
	points, err := history.Read(
		ctx,
		appID,
		[]string{updateID, emptyUpdateID},
		bucket.Add(-time.Minute),
		bucket.Add(time.Minute),
	)
	require.NoError(t, err)
	require.Len(t, points[updateID], 1)
	require.Empty(t, points[emptyUpdateID])
	assert.EqualValues(t, 20, points[updateID][0].DevicesOnUpdate)
	require.NotNil(t, points[updateID][0].HealthPercent)
	assert.InDelta(t, 90, *points[updateID][0].HealthPercent, 0.001)
}
