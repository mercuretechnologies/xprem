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

	"expo-open-ota/internal/database/clickhouse"

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

// The whole point of the counters: they must say exactly what rebuilding the
// grid says. Anything else and the chart changes shape the day a deployment
// accumulates enough history to switch paths, which is the one thing a
// performance change must never do.
//
// Every bucket the read asks for is captured, then both paths answer the same
// question and are compared point by point.
func TestPrecountedSegmentsMatchTheLiveGrid(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	appID := uuid.NewString()
	watched, other := uuid.NewString(), uuid.NewString()

	// Aligned on the capture bucket so the instants the worker records are the
	// instants the read asks for.
	start := time.Now().UTC().Truncate(healthSegmentBucket).Add(-30 * healthSegmentBucket)
	to := start.Add(20 * healthSegmentBucket)

	devices := make([]string, 12)
	for i := range devices {
		devices[i] = uuid.NewString()
	}
	events := []healthEvent{}
	for i, device := range devices {
		// Three OS versions, adopted at staggered times so the series moves
		// rather than being flat, which is what makes a mismatch visible.
		events = append(events, healthEvent{
			id: uint64(i + 1), eventType: "first_seen", device: device,
			update: watched, osVersion: fmt.Sprintf("1%d", i%3),
			occurredAt: start.Add(time.Duration(i) * healthSegmentBucket),
		})
	}
	// One device fails midway, one leaves for another update, and one never
	// touches the watched update at all.
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

// A capture is idempotent: the worker recaptures the current bucket every five
// minutes and a replica restart replays it, so a bucket written twice must not
// count its devices twice. The ReplacingMergeTree collapses on the sorting key
// and the read takes the latest capture, but neither is worth trusting without
// asking.
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

// A coarse window rolls five-minute samples up by taking the last one in each
// bucket, never their sum. Summing is the obvious mistake and it is silent: a
// fleet of ten devices would read as thirty over a fifteen-minute bucket, and
// the chart would simply look like the app grew.
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
	// Three consecutive five-minute samples, all showing the same single
	// device, rolled up into one fifteen-minute bucket.
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

// Coverage decides which path answers, so it has to be honest about an empty
// table. min() over no rows comes back as the zero DateTime, which compares
// as before every window and would claim coverage the table does not have,
// sending every read to an empty answer instead of to the grid.
func TestCoverageIsRefusedWhenNothingHasBeenCaptured(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	history := NewHealthHistory(nil, engine)
	now := time.Now().UTC()

	covered, err := history.segmentSnapshotsCover(ctx, uuid.NewString(), now.Add(-time.Hour), now)
	require.NoError(t, err)
	require.False(t, covered, "an app with no captures must fall back to the live grid")
}

// And it must refuse a window that starts before the counters do, which is
// every window reaching back past the upgrade that turned them on.
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

// The failure a start-of-window check cannot see: the counters reach back far
// enough but the middle is missing, because the process was down. Drawn from
// the counters that window has a gap where the events still hold a fleet, and
// a gap on a health chart reads as every device having left.
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

	// Captured throughout, except for a stretch in the middle: the shape a
	// restart leaves behind.
	for i := 0; i < 24; i++ {
		if i >= 8 && i < 16 {
			continue
		}
		require.NoError(t, history.captureSegmentBucket(ctx, start.Add(time.Duration(i)*healthSegmentBucket)))
	}

	covered, err := history.segmentSnapshotsCover(ctx, appID, start, end)
	require.NoError(t, err)
	require.False(t, covered, "a third of the window missing must send the read back to the events")

	// And the same window once the hole is filled, so the test is about the
	// hole and not about the window being unservable in general.
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

// The equivalence above uses three segments, so a ceiling on how many are kept
// would have passed it unnoticed. This is the same comparison on a dimension
// with hundreds of values, which is the shape deviceModel actually has:
// measured at some 1 700 per update on a fleet of a million.
//
// It asserts plain equality, which is the whole reason there is no ceiling. A
// ceiling would have made the counters keep the largest segments and sum the
// rest, and this test would then have had to assert something weaker than "the
// same answer" for exactly the dimension where the answer matters most.
func TestPrecountedSegmentsMatchTheLiveGridOnALongTail(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	appID := uuid.NewString()
	update := uuid.NewString()
	at := time.Now().UTC().Truncate(healthSegmentBucket)

	// A handful of large segments and a long tail of one-device ones.
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

	// And what the reader ends up seeing, which is the eight the handler keeps.
	require.Equal(t,
		TrimSegments(live, maxHealthSegments),
		TrimSegments(precounted, maxHealthSegments),
		"the chart draws the same eight segments from either path")
}

// The equivalences above start their window on a capture boundary, so neither
// exercised what happens when it does not. It usually does not: the live view
// snaps its window to the minute while captures happen every five, so four
// starts out of five land between two samples.
//
// Rounding a sample down to the instant it follows answered an instant with a
// sample taken after it, and left the opening instant with nothing at all.
// This walks every offset inside one capture interval and asks both paths the
// same question at each.
func TestPrecountedSegmentsAgreeWhenTheWindowStartsOffTheCaptureGrid(t *testing.T) {
	engine, ctx := newSegmentFixture(t)
	appID := uuid.NewString()
	update := uuid.NewString()
	aligned := time.Now().UTC().Truncate(healthSegmentBucket).Add(-12 * healthSegmentBucket)

	// A fleet that changes over the window, so a point answered by the wrong
	// sample carries a different number rather than the same one by luck.
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
