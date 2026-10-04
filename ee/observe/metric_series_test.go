// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"xprem/internal/database"
	"xprem/internal/database/clickhouse"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"
)

func metricSeriesExplorer(t *testing.T) *Explorer {
	t.Helper()
	chURL, pgURL := requireLiveStores(t)
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(pgURL)
	clickhouse.RunDBMigrations(chURL, pgURL)
	pool, err := pgxpool.New(context.Background(), pgURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	engine, err := clickhouse.NewClickHouseEngine(context.Background(), chURL)
	require.NoError(t, err)
	t.Cleanup(func() { engine.Close() })
	return NewExplorer(&database.Engine{Queries: pgdb.New(pool), DB: pool}, engine)
}

func requireMetricWindow(t *testing.T, response any, query ExplorerQuery) {
	t.Helper()
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	var window struct {
		From          time.Time `json:"from"`
		To            time.Time `json:"to"`
		BucketSeconds int64     `json:"bucketSeconds"`
	}
	require.NoError(t, json.Unmarshal(encoded, &window))
	require.True(t, window.From.Equal(query.From), "response from must describe the queried window")
	require.True(t, window.To.Equal(query.To), "response to must describe the queried window")
	require.Equal(t, max(int64(query.Bucket/time.Second), 1), window.BucketSeconds)
}

func requireMetricDensity(t *testing.T, point ObserveMetricPoint, samples, devices uint64) {
	t.Helper()
	encoded, err := json.Marshal(point)
	require.NoError(t, err)
	var density struct {
		Samples uint64 `json:"samples"`
		Devices uint64 `json:"devices"`
	}
	require.NoError(t, json.Unmarshal(encoded, &density))
	require.Equal(t, samples, density.Samples)
	require.Equal(t, devices, density.Devices)
}

func TestMetricSeriesLiveZeroBucketsAndDensity(t *testing.T) {
	explorer := metricSeriesExplorer(t)
	ctx := context.Background()
	appID, updateID := uuid.NewString(), uuid.NewString()
	to := time.Now().UTC().Truncate(time.Hour).Add(20 * time.Minute)
	at := to.Add(-10 * time.Minute)
	var fixture []MetricRow
	for range 5 {
		fixture = append(fixture, MetricRow{
			Envelope: Envelope{AppID: appID, UpdateID: updateID, EASClientID: uuid.NewString(),
				SessionID: uuid.NewString(), Platform: "ios", Timestamp: at, ContentKey: uuid.New()},
			MetricName: "expo.app_startup.cold_launch_time", Value: 0,
		})
	}
	// Six samples from five devices, plus a retried batch record.
	extra := fixture[0]
	extra.Timestamp, extra.ContentKey = at.Add(time.Minute), uuid.New()
	fixture = append(fixture, extra, fixture[0])
	for _, olderAt := range []time.Time{to.Add(-80 * time.Minute), to.Add(-12*time.Hour - 20*time.Minute)} {
		older := fixture[0]
		older.Timestamp, older.ContentKey, older.Value = olderAt, uuid.New(), 1
		fixture = append(fixture, older)
	}
	foreignApp, foreignUpdate := fixture[0], fixture[0]
	foreignApp.AppID, foreignApp.ContentKey, foreignApp.Value = uuid.NewString(), uuid.New(), 100
	foreignUpdate.UpdateID, foreignUpdate.ContentKey, foreignUpdate.Value = uuid.NewString(), uuid.New(), 100
	fixture = append(fixture, foreignApp, foreignUpdate)
	require.NoError(t, NewClickHouseTelemetrySink(explorer.clickhouse).InsertMetrics(ctx, fixture))

	for _, span := range []time.Duration{time.Hour, 24 * time.Hour} {
		t.Run(span.String(), func(t *testing.T) {
			query := ExplorerQuery{From: to.Add(-span), To: to, Bucket: Bucket(span), UpdateIDs: []string{updateID}}
			overview, err := explorer.readOverview(ctx, appID, query)
			require.NoError(t, err)
			require.Len(t, overview.Metrics, 1)
			points := overview.Metrics[0].Points
			require.NotEmpty(t, points)
			last := points[len(points)-1]
			require.True(t, last.Timestamp.Equal(at.Truncate(query.Bucket)))
			require.Zero(t, last.Value, "a real zero measurement must survive both bucket widths")
			requireMetricDensity(t, last, 6, 5)
			requireMetricWindow(t, overview, query)
			breakdown, err := explorer.readBreakdown(ctx, appID, BreakdownQuery{
				ExplorerQuery: query, Metric: "cold-launch", Dimension: "platform", WithPoints: true,
			})
			require.NoError(t, err)
			require.Len(t, breakdown.Segments, 1)
			segment := breakdown.Segments[0]
			require.Equal(t, "ios", segment.Value)
			require.NotEmpty(t, segment.Points)
			last = segment.Points[len(segment.Points)-1]
			require.Zero(t, last.Value)
			requireMetricDensity(t, last, 6, 5)
			requireMetricWindow(t, breakdown, query)
		})
	}
}

func TestMetricSeriesLiveEmptyAndUnavailableWindows(t *testing.T) {
	explorer := metricSeriesExplorer(t)
	appID := uuid.NewString()
	to := time.Now().UTC().Truncate(time.Hour).Add(20 * time.Minute)
	base := ExplorerQuery{From: to.Add(-24 * time.Hour), To: to, Bucket: 15 * time.Minute}
	for _, path := range []string{"empty", "empty group", "empty cohort", "unavailable"} {
		t.Run(path, func(t *testing.T) {
			query, reader := base, explorer
			switch path {
			case "empty group":
				query.UpdateGroupIDs = []string{uuid.NewString()}
			case "empty cohort":
				query.MetadataFilter = [][]byte{[]byte(`{"test":"missing"}`)}
			case "unavailable":
				reader = &Explorer{postgres: explorer.postgres}
			}
			overview, err := reader.readOverview(context.Background(), appID, query)
			require.NoError(t, err)
			require.Empty(t, overview.Metrics)
			require.Equal(t, path != "unavailable", overview.Available)
			requireMetricWindow(t, overview, query)
			breakdown, err := reader.readBreakdown(context.Background(), appID, BreakdownQuery{
				ExplorerQuery: query, Metric: "cold-launch", Dimension: "platform", WithPoints: true,
			})
			require.NoError(t, err)
			require.Empty(t, breakdown.Segments)
			require.Equal(t, path != "unavailable", breakdown.Available)
			requireMetricWindow(t, breakdown, query)
		})
	}
}

func TestMetricSeriesUnavailableHandlerWindows(t *testing.T) {
	to := time.Now().UTC().Truncate(time.Hour).Add(20 * time.Minute)
	query := ExplorerQuery{From: to.Add(-24 * time.Hour), To: to, Bucket: 15 * time.Minute}
	window := "from=" + query.From.Format(time.RFC3339) + "&to=" + query.To.Format(time.RFC3339)
	for _, path := range []string{"/observe/overview?", "/observe/breakdown?metric=cold-launch&dimension=platform&"} {
		recorder := serveExplorer(NewExplorerHandler(nil, nil), path+window)
		require.Equal(t, http.StatusOK, recorder.Code)
		var response map[string]any
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		requireMetricWindow(t, response, query)
	}
}
