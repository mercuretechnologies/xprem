// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"xprem/internal/database/clickhouse"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newSegmentFixture(t *testing.T) (*clickhouse.Engine, context.Context) {
	t.Helper()
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)
	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	t.Cleanup(func() { engine.Close() })
	return engine, ctx
}

// The precounted read must return the same chart as rebuilding it live.
func TestPrecountedSegmentsMatchTheLiveGrid(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	appID := uuid.NewString()
	watched, other := uuid.NewString(), uuid.NewString()

	start := time.Now().UTC().Truncate(healthSegmentBucket).Add(-30 * healthSegmentBucket)
	to := start.Add(20 * healthSegmentBucket)

	devices := make([]string, 12)
	for i := range devices {
		devices[i] = uuid.NewString()
	}
	events := []healthEvent{}
	for i, device := range devices {
		events = append(events, healthEvent{
			id: uint64(i + 1), eventType: "first_seen", device: device,
			update: watched, osVersion: fmt.Sprintf("1%d", i%3),
			occurredAt: start.Add(time.Duration(i) * healthSegmentBucket),
		})
	}
	events = append(events,
		healthEvent{id: 100, eventType: "failure", device: devices[0], update: watched,
			osVersion: "10", occurredAt: start.Add(5 * healthSegmentBucket)},
		healthEvent{id: 101, eventType: "switched", device: devices[1], update: other,
			osVersion: "11", occurredAt: start.Add(9 * healthSegmentBucket)},
		healthEvent{id: 102, eventType: "first_seen", device: uuid.NewString(), update: other,
			osVersion: "12", occurredAt: start},
	)
	insertHealthEvents(t, engine, appID, events)

	history := NewHealthHistory(nil, engine)
	_, step := history.gridSteps(start, to)
	for at := start; !at.After(to); at = at.Add(time.Duration(step) * time.Second) {
		require.NoError(t, history.captureSegmentBucket(ctx, at))
	}

	live, err := history.readSegmentGrid(ctx, appID, []string{watched}, "osVersion", start, to, step,
		int64(to.Sub(start)/(time.Duration(step)*time.Second))+1)
	require.NoError(t, err)
	precounted, err := history.readSegmentSnapshots(ctx, appID, []string{watched}, "osVersion", start, to, step)
	require.NoError(t, err)

	require.NotEmpty(t, live, "the scenario must produce a chart at all")
	require.Equal(t, sortedKeys(live), sortedKeys(precounted), "both paths must find the same segments")
	for segment, livePoints := range live {
		require.Equal(t, livePoints, precounted[segment],
			"segment %q must read identically whichever path answered", segment)
	}
}

// A bucket captured twice must not count its devices twice.
func TestRecapturingABucketDoesNotDoubleCount(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	appID := uuid.NewString()
	update := uuid.NewString()
	at := time.Now().UTC().Truncate(healthSegmentBucket).Add(-healthSegmentBucket)

	insertHealthEvents(t, engine, appID, []healthEvent{
		{1, "first_seen", uuid.NewString(), update, "17", at.Add(-time.Minute)},
		{2, "first_seen", uuid.NewString(), update, "17", at.Add(-time.Minute)},
	})

	history := NewHealthHistory(nil, engine)
	require.NoError(t, history.captureSegmentBucket(ctx, at))
	require.NoError(t, history.captureSegmentBucket(ctx, at))

	points, err := history.readSegmentSnapshots(ctx, appID, []string{update}, "osVersion",
		at, at.Add(healthSegmentBucket), int64(healthSegmentBucket.Seconds()))
	require.NoError(t, err)
	require.Len(t, points["17"], 1)
	require.Equal(t, uint64(2), points["17"][0].DevicesOnUpdate,
		"two devices captured twice are still two devices")
}

// A coarse window rolls up samples by taking the last one in the bucket, never their sum.
func TestCoarseBucketsTakeTheLastSampleNotTheSum(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	appID := uuid.NewString()
	update := uuid.NewString()
	start := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)

	device := uuid.NewString()
	insertHealthEvents(t, engine, appID, []healthEvent{
		{1, "first_seen", device, update, "17", start.Add(-time.Minute)},
	})

	history := NewHealthHistory(nil, engine)
	for i := 0; i < 3; i++ {
		require.NoError(t, history.captureSegmentBucket(ctx, start.Add(time.Duration(i)*healthSegmentBucket)))
	}

	points, err := history.readSegmentSnapshots(ctx, appID, []string{update}, "osVersion",
		start, start.Add(15*time.Minute), int64((15 * time.Minute).Seconds()))
	require.NoError(t, err)
	require.NotEmpty(t, points["17"])
	for _, point := range points["17"] {
		require.Equal(t, uint64(1), point.DevicesOnUpdate,
			"one device sampled three times is one device, not three")
	}
}

// min() over an empty table returns the zero DateTime, which must not be read as coverage.
func TestCoverageIsRefusedWhenNothingHasBeenCaptured(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	history := NewHealthHistory(nil, engine)
	now := time.Now().UTC()

	covered, err := history.segmentSnapshotsCover(ctx, uuid.NewString(), now.Add(-time.Hour), now)
	require.NoError(t, err)
	require.False(t, covered, "an app with no captures must fall back to the live grid")
}

// A window starting before the first capture is not covered.
func TestCoverageIsRefusedForAWindowOlderThanTheCounters(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	appID := uuid.NewString()
	update := uuid.NewString()
	at := time.Now().UTC().Truncate(healthSegmentBucket)

	insertHealthEvents(t, engine, appID, []healthEvent{
		{1, "first_seen", uuid.NewString(), update, "17", at.Add(-time.Minute)},
	})
	history := NewHealthHistory(nil, engine)
	require.NoError(t, history.captureSegmentBucket(ctx, at))

	covered, err := history.segmentSnapshotsCover(ctx, appID, at.Add(-24*time.Hour), at.Add(healthSegmentBucket))
	require.NoError(t, err)
	require.False(t, covered, "a window older than the first capture cannot be served from it")

	covered, err = history.segmentSnapshotsCover(ctx, appID, at, at.Add(healthSegmentBucket))
	require.NoError(t, err)
	require.True(t, covered, "a window starting at the first capture is covered")
}

// A window covered at both ends but missing captures in the middle is not covered.
func TestCoverageIsRefusedWhenTheWindowHasAHole(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	appID := uuid.NewString()
	update := uuid.NewString()
	start := time.Now().UTC().Truncate(healthSegmentBucket).Add(-24 * healthSegmentBucket)
	end := start.Add(24 * healthSegmentBucket)

	insertHealthEvents(t, engine, appID, []healthEvent{
		{1, "first_seen", uuid.NewString(), update, "17", start.Add(-time.Minute)},
	})
	history := NewHealthHistory(nil, engine)

	for i := 0; i < 24; i++ {
		if i >= 8 && i < 16 {
			continue
		}
		require.NoError(t, history.captureSegmentBucket(ctx, start.Add(time.Duration(i)*healthSegmentBucket)))
	}

	covered, err := history.segmentSnapshotsCover(ctx, appID, start, end)
	require.NoError(t, err)
	require.False(t, covered, "a third of the window missing must send the read back to the events")

	for i := 8; i < 16; i++ {
		require.NoError(t, history.captureSegmentBucket(ctx, start.Add(time.Duration(i)*healthSegmentBucket)))
	}
	covered, err = history.segmentSnapshotsCover(ctx, appID, start, end)
	require.NoError(t, err)
	require.True(t, covered, "a complete window is answered from the counters")
}

func sortedKeys(bySegment map[string][]HealthSegmentPoint) []string {
	keys := make([]string, 0, len(bySegment))
	for key := range bySegment {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// Same comparison as above, but with a long tail of hundreds of segments.
func TestPrecountedSegmentsMatchTheLiveGridOnALongTail(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	appID := uuid.NewString()
	update := uuid.NewString()
	at := time.Now().UTC().Truncate(healthSegmentBucket)

	events := []healthEvent{}
	id := uint64(0)
	add := func(version string, count int) {
		for i := 0; i < count; i++ {
			id++
			events = append(events, healthEvent{
				id: id, eventType: "first_seen", device: uuid.NewString(),
				update: update, osVersion: version, occurredAt: at.Add(-time.Minute),
			})
		}
	}
	for i, count := range []int{400, 300, 200, 100, 80, 60, 40, 20} {
		add(fmt.Sprintf("big-%d", i), count)
	}
	const tail = 210
	for i := 0; i < tail; i++ {
		add(fmt.Sprintf("tail-%03d", i), 1)
	}
	insertHealthEvents(t, engine, appID, events)

	history := NewHealthHistory(nil, engine)
	require.NoError(t, history.captureSegmentBucket(ctx, at))

	step := int64(healthSegmentBucket.Seconds())
	live, err := history.readSegmentGrid(ctx, appID, []string{update}, "osVersion",
		at, at.Add(healthSegmentBucket), step, 1)
	require.NoError(t, err)
	precounted, err := history.readSegmentSnapshots(ctx, appID, []string{update}, "osVersion",
		at, at.Add(healthSegmentBucket), step)
	require.NoError(t, err)

	require.Greater(t, len(live), 200, "the fixture must have a tail worth the name")
	require.Equal(t, sortedKeys(live), sortedKeys(precounted),
		"every segment the events know must have been counted, however small")
	for segment, points := range live {
		require.Equal(t, points, precounted[segment],
			"segment %q must read identically whichever path answered", segment)
	}

	require.Equal(t,
		TrimSegments(live, maxHealthSegments),
		TrimSegments(precounted, maxHealthSegments),
		"the chart draws the same eight segments from either path")
}

// Walks every offset inside one capture interval, since a window need not start on a capture boundary.
func TestPrecountedSegmentsAgreeWhenTheWindowStartsOffTheCaptureGrid(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	appID := uuid.NewString()
	update := uuid.NewString()
	aligned := time.Now().UTC().Truncate(healthSegmentBucket).Add(-12 * healthSegmentBucket)

	events := []healthEvent{}
	for i := 0; i < 9; i++ {
		events = append(events, healthEvent{
			id: uint64(i + 1), eventType: "first_seen", device: uuid.NewString(),
			update: update, osVersion: "17",
			occurredAt: aligned.Add(time.Duration(i) * healthSegmentBucket),
		})
	}
	insertHealthEvents(t, engine, appID, events)

	history := NewHealthHistory(nil, engine)
	for i := -1; i <= 12; i++ {
		require.NoError(t, history.captureSegmentBucket(ctx, aligned.Add(time.Duration(i)*healthSegmentBucket)))
	}

	for offset := time.Duration(0); offset < healthSegmentBucket; offset += time.Minute {
		t.Run(offset.String(), func(t *testing.T) {
			from := aligned.Add(offset)
			to := from.Add(8 * healthSegmentBucket)
			_, step := history.gridSteps(from, to)

			live, err := history.readSegmentGrid(ctx, appID, []string{update}, "osVersion",
				from, to, step, int64(to.Sub(from)/(time.Duration(step)*time.Second))+1)
			require.NoError(t, err)
			precounted, err := history.readSegmentSnapshots(ctx, appID, []string{update}, "osVersion",
				from, to, step)
			require.NoError(t, err)

			require.NotEmpty(t, live["17"], "the scenario must draw something")
			require.Equal(t, live["17"], precounted["17"],
				"a window starting %s past a capture must read the same either way", offset)
		})
	}
}

// A coarse window holds several captures per drawn point; every series in that
// point must be read from the same one, not each series' own latest sample.
func TestOneCaptureAnswersEachDrawnPointAcrossUpdates(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	appID := uuid.NewString()
	from, to := uuid.NewString(), uuid.NewString()
	start := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)

	devices := make([]string, 10)
	events := []healthEvent{}
	for i := range devices {
		devices[i] = uuid.NewString()
		events = append(events, healthEvent{
			id: uint64(i + 1), eventType: "first_seen", device: devices[i],
			update: from, osVersion: "17", occurredAt: start.Add(-time.Minute),
		})
		events = append(events, healthEvent{
			id: uint64(100 + i), eventType: "switched", device: devices[i],
			update: to, osVersion: "17", occurredAt: start.Add(6 * time.Minute),
		})
	}
	insertHealthEvents(t, engine, appID, events)

	history := NewHealthHistory(nil, engine)
	for i := 0; i < 3; i++ {
		require.NoError(t, history.captureSegmentBucket(ctx, start.Add(time.Duration(i)*healthSegmentBucket)))
	}

	points, err := history.readSegmentSnapshots(ctx, appID, []string{from, to}, "osVersion",
		start, start.Add(15*time.Minute), int64((15 * time.Minute).Seconds()))
	require.NoError(t, err)
	require.NotEmpty(t, points["17"])
	for _, point := range points["17"] {
		require.LessOrEqual(t, point.DevicesOnUpdate, uint64(len(devices)),
			"summing two updates read at different instants counts the movers twice")
	}
}

// A rewrite can overwrite a cell but never remove one, so a segment that
// empties between two captures must not keep its stale count from the first.
func TestARecaptureDoesNotLeaveAStaleCellBehind(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	appID := uuid.NewString()
	from, to := uuid.NewString(), uuid.NewString()
	at := time.Now().UTC().Truncate(healthSegmentBucket)

	devices := make([]string, 6)
	events := []healthEvent{}
	for i := range devices {
		devices[i] = uuid.NewString()
		events = append(events, healthEvent{
			id: uint64(i + 1), eventType: "first_seen", device: devices[i],
			update: from, osVersion: "17", occurredAt: at.Add(-2 * time.Minute),
		})
	}
	insertHealthEvents(t, engine, appID, events)
	require.NoError(t, history(engine).captureSegmentBucket(ctx, at))

	// Late-arriving event: dated before the bucket, ingested after it.
	late := []healthEvent{}
	for i, device := range devices {
		late = append(late, healthEvent{
			id: uint64(100 + i), eventType: "switched", device: device,
			update: to, osVersion: "17", occurredAt: at.Add(-time.Minute),
		})
	}
	insertHealthEvents(t, engine, appID, late)
	require.NoError(t, history(engine).captureSegmentBucket(ctx, at))

	points, err := history(engine).readSegmentSnapshots(ctx, appID, []string{from, to}, "osVersion",
		at, at.Add(healthSegmentBucket), int64(healthSegmentBucket.Seconds()))
	require.NoError(t, err)
	require.NotEmpty(t, points["17"])
	for _, point := range points["17"] {
		require.Equal(t, uint64(len(devices)), point.DevicesOnUpdate,
			"the emptied update must not keep its count alongside the one that took its devices")
	}
}

func history(engine *clickhouse.Engine) *HealthHistory { return NewHealthHistory(nil, engine) }
