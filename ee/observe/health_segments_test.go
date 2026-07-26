// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"os"
	"testing"
	"time"

	"expo-open-ota/internal/database/clickhouse"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// healthEvent is one row of device_health_events, written straight in: this
// reads ClickHouse only, and going through the outbox would test the delivery
// path instead of the query.
type healthEvent struct {
	id         uint64
	eventType  string
	device     string
	update     string
	osVersion  string
	occurredAt time.Time
}

func insertHealthEvents(t *testing.T, engine *clickhouse.Engine, appID string, events []healthEvent) {
	t.Helper()
	batch, err := engine.Conn.PrepareBatch(context.Background(), `INSERT INTO device_health_events
		(outbox_id, event_type, app_id, eas_client_id, update_id, occurred_at, os_version)`)
	require.NoError(t, err)
	for _, event := range events {
		require.NoError(t, batch.Append(
			event.id, event.eventType, appID, event.device, event.update, event.occurredAt, event.osVersion))
	}
	require.NoError(t, batch.Send())
}

func pointAt(t *testing.T, points []HealthSegmentPoint, at time.Time) *HealthSegmentPoint {
	t.Helper()
	for i := range points {
		if points[i].Timestamp.Equal(at) {
			return &points[i]
		}
	}
	return nil
}

// The segment of a bucket must be what the device was AT that bucket. Reading
// it from the device's latest known value moved a device's whole history under
// its new label the day it upgraded, which is precisely backwards for a chart
// whose job is to show when things changed.
func TestReadBySegmentLabelsBucketsWithTheirOwnState(t *testing.T) {
	chURL := os.Getenv("TEST_CLICKHOUSE_URL")
	pgURL := os.Getenv("TEST_DATABASE_URL")
	if chURL == "" || pgURL == "" {
		t.Skip("TEST_CLICKHOUSE_URL and TEST_DATABASE_URL not both set; skipping segmented history test")
	}
	clickhouse.RunDBMigrations(chURL, pgURL)

	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()

	appID := uuid.NewString()
	watched, nextUpdate, unrelated := uuid.NewString(), uuid.NewString(), uuid.NewString()
	upgrader, stable, elsewhere := uuid.NewString(), uuid.NewString(), uuid.NewString()

	start := time.Now().UTC().Truncate(time.Minute).Add(-10 * time.Minute)
	switched := start.Add(6 * time.Minute)

	insertHealthEvents(t, engine, appID, []healthEvent{
		// Adopts the watched update on iOS 17, then moves to the next update
		// having upgraded to 18 in the meantime.
		{1, "first_seen", upgrader, watched, "17", start},
		{2, "switched", upgrader, nextUpdate, "18", switched},
		// Already on 18 from the start, and stays on the watched update.
		{3, "first_seen", stable, watched, "18", start},
		// Never ran the watched update: must not enter the grid at all.
		{4, "first_seen", elsewhere, unrelated, "17", start},
	})

	history := NewHealthHistory(nil, engine)
	bySegment, err := history.ReadBySegment(ctx, appID, []string{watched}, "osVersion",
		start, start.Add(9*time.Minute))
	require.NoError(t, err)

	require.Contains(t, bySegment, "17", "the early buckets must keep the version the device ran then")
	require.Contains(t, bySegment, "18")

	// Before the switch: one device on each version, both running the update.
	early := start.Add(2 * time.Minute)
	require.NotNil(t, pointAt(t, bySegment["17"], early))
	require.EqualValues(t, 1, pointAt(t, bySegment["17"], early).DevicesOnUpdate)
	require.EqualValues(t, 1, pointAt(t, bySegment["18"], early).DevicesOnUpdate)

	// After it, the upgrader has left the watched update, so 17 has no bucket
	// left at all rather than a relabelled one.
	late := start.Add(8 * time.Minute)
	require.Nil(t, pointAt(t, bySegment["17"], late), "the device left the update, it must not linger under any label")
	require.EqualValues(t, 1, pointAt(t, bySegment["18"], late).DevicesOnUpdate)

	// A device that never ran a requested update contributes nothing, which is
	// also why narrowing the population to those devices is free.
	var total uint64
	for _, points := range bySegment {
		for _, point := range points {
			total += point.DevicesOnUpdate
		}
	}
	// 10 buckets. The upgrader is on the watched update for the first 6, the
	// stable device for all 10.
	require.EqualValues(t, 16, total)
}
