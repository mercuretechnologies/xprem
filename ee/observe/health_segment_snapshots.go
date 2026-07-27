// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"fmt"
	"log"
	"time"

	"expo-open-ota/internal/database/postgres"
)

const (
	// The finest bucket any window offers, so a capture is never coarser than
	// something the chart can show, and never finer than something it can.
	healthSegmentBucket = 5 * time.Minute

	// segmentSnapshotAdvisoryLockID elects one replica to build the grid.
	// Separate from the other two so a slow capture never blocks outbox
	// delivery, which is what keeps the unsplit chart responsive.
	segmentSnapshotAdvisoryLockID = 745103624

	// How complete a window has to be before the counters answer it rather
	// than the grid. Chosen to tolerate the handful of buckets a deploy or a
	// slow capture costs, and to refuse a window with a real outage in it.
	segmentCoverageMinPercent = 95

	// A capture is bounded work but it is still a grid over the whole fleet.
	// It runs on a timer with nobody waiting, so it gets room, but not
	// unlimited room: a capture still running when the next one is due is a
	// capture that will never catch up.
	segmentCaptureTimeout = 4 * time.Minute
)

// captureSegmentSnapshots counts, for the bucket that just closed, how many
// devices were on each update in each segment of each dimension.
//
// It is the read query with the window collapsed to a single instant. That is
// the entire optimisation: the read paid population times buckets on every
// page view, and this pays population times one, once, for every viewer.
//
// The INSERT and the SELECT are one statement so the rows a capture produces
// never leave ClickHouse.
//
// Every segment is written, with no ceiling and no summary row. A ceiling was
// tried and removed: it saved writes at the price of three separate defects.
// The tail folded into one row large enough to take a slot on a chart that
// draws eight, so a precounted read showed seven segments and a lump where a
// live read showed eight. Membership was recomputed per bucket, so a segment
// that was large in September, small in October and large again in November
// had no row for October and its curve showed a hole where its devices had
// merely moved into the summary. And the values behind that summary could not
// be named, so nothing could be clicked or filtered.
//
// What it cost to keep them is bounded by the read, which is the operation
// that repeats: a read touches only the segments present between its two
// instants, seeking on (app_id, update_id, dimension, bucket), so it stays two
// to three orders of magnitude under rebuilding the grid however wide the
// dimension is. Writes grow with updates that still carry devices times the
// cardinality of the dimension, which an operator bounds with a TTL on this
// table rather than with a cut nobody can undo.
func (h *HealthHistory) captureSegmentSnapshots(ctx context.Context) {
	if h.clickhouse == nil {
		return
	}
	// One replica at a time, for the same reason the unsplit snapshot elects
	// one: the numbers are a property of the fleet, not of the replica that
	// happens to read them, and ten replicas building ten identical grids is
	// ten times the work for one answer.
	release, locked, err := postgres.TryAdvisoryLock(ctx, h.postgres.DB, segmentSnapshotAdvisoryLockID, "health segment snapshot")
	if err != nil {
		log.Printf("observe: taking the segment snapshot lock failed: %v", err)
		return
	}
	if !locked {
		return
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, segmentCaptureTimeout)
	defer cancel()

	if err := h.captureSegmentBucket(ctx, time.Now().UTC().Truncate(healthSegmentBucket)); err != nil {
		log.Printf("observe: capturing segmented health failed: %v", err)
	}
}

// captureSegmentBucket counts one bucket. Split out from the caller above so
// the instant is a parameter rather than the clock: the capture is otherwise
// only ever exercisable at whatever five-minute mark a test happens to run at.
func (h *HealthHistory) captureSegmentBucket(ctx context.Context, bucket time.Time) error {
	return h.clickhouse.Conn.Exec(ctx, segmentCaptureSQL, bucket, bucket, bucket)
}

// segmentCaptureSQL mirrors readBySegment CTE for CTE, with three deliberate
// differences: the window is one instant instead of a range, no update filter
// (the read picks its updates out of the result), and every dimension is
// counted in the same pass rather than one per call.
//
// Counting all eight at once is why this is affordable at all. The grid and
// its two ASOF joins are the expensive part; the ARRAY JOIN that follows turns
// one device row into eight labelled rows over an already-joined result, so
// the eighth dimension costs a group-by rather than another grid.
const segmentCaptureSQL = `
INSERT INTO update_health_segment_snapshots
    (app_id, update_id, dimension, bucket, segment, captured_at, devices_on_update, faulty_devices)
WITH
  adoptions AS (
    SELECT app_id, eas_client_id, update_id, occurred_at, device_model, os_version,
           os_name, country_code, app_version, platform, runtime_version, branch
    FROM device_health_events
    WHERE event_type IN ('first_seen', 'switched') AND occurred_at <= ?
  ),
  faults AS (
    SELECT app_id, eas_client_id, update_id, occurred_at,
           event_type = 'failure' AS faulty
    FROM device_health_events
    WHERE event_type IN ('failure', 'recovered') AND occurred_at <= ?
  ),
  grid AS (
    SELECT DISTINCT app_id, eas_client_id, toDateTime(?) AS bucket FROM adoptions
  ),
  running AS (
    SELECT g.bucket AS bucket, g.app_id AS app_id, g.eas_client_id AS eas_client_id,
           a.update_id AS update_id, a.device_model AS device_model,
           a.os_version AS os_version, a.os_name AS os_name,
           a.country_code AS country_code, a.app_version AS app_version,
           a.platform AS platform, a.runtime_version AS runtime_version,
           a.branch AS branch
    FROM grid g
    ASOF LEFT JOIN adoptions a
      ON g.app_id = a.app_id AND g.eas_client_id = a.eas_client_id
         AND g.bucket >= a.occurred_at
  ),
  failing AS (
    -- Faults join on the update as well as the device. Without that, a device
    -- that crashed on an old update stays faulty for every update it runs
    -- afterwards, and for native crashes that never clears: only runtime
    -- issues ever emit a 'recovered' event.
    SELECT r.*, f.faulty AS faulty
    FROM running r
    ASOF LEFT JOIN faults f
      ON r.app_id = f.app_id AND r.eas_client_id = f.eas_client_id
         AND r.update_id = f.update_id AND r.bucket >= f.occurred_at
  ),
  counted AS (
    SELECT app_id, update_id, bucket, dim.1 AS dimension, dim.2 AS segment,
           count() AS devices, countIf(faulty = 1) AS faulty
    FROM failing
    ARRAY JOIN [('deviceModel', device_model), ('osVersion', os_version),
                ('osName', os_name), ('country', country_code),
                ('appVersion', app_version), ('platform', platform),
                ('runtimeVersion', runtime_version), ('branch', branch)] AS dim
    GROUP BY app_id, update_id, bucket, dimension, segment
  )
SELECT app_id, update_id, dimension, bucket, segment,
       now64(3) AS captured_at, devices, faulty
FROM counted`

// readSegmentSnapshots serves the chart from the counters, rolling the
// five-minute samples up to whatever bucket the window asks for.
//
// Rolled up by taking the LAST sample in each coarse bucket, never the sum:
// these count devices present at an instant, and a device sits in every bucket
// it was alive for, so adding three five-minute samples would report three
// times the fleet. Summed across updates, though, because a device is on
// exactly one update at a time and those sets are disjoint.
//
// argMax on (bucket, captured_at) does the rollup and the ReplacingMergeTree
// deduplication in one pass: latest bucket wins, and within one bucket the
// latest capture wins.
func (h *HealthHistory) readSegmentSnapshots(
	ctx context.Context,
	appID string,
	updateIDs []string,
	dimension string,
	from, to time.Time,
	step int64,
) (map[string][]HealthSegmentPoint, error) {
	rows, err := h.clickhouse.Conn.Query(ctx, `
		SELECT t, segment, sum(devices) AS devices, sum(faulty) AS faulty
		FROM (
		  SELECT toDateTime(?) + toIntervalSecond(intDiv(toUInt32(bucket) - toUInt32(toDateTime(?)), ?) * ?) AS t,
		         update_id, segment,
		         argMax(devices_on_update, (bucket, captured_at)) AS devices,
		         argMax(faulty_devices, (bucket, captured_at)) AS faulty
		  FROM update_health_segment_snapshots
		  WHERE app_id = ? AND dimension = ? AND toString(update_id) IN ?
		    AND bucket >= ? AND bucket <= ?
		  GROUP BY t, update_id, segment
		)
		GROUP BY t, segment
		ORDER BY t`,
		from.UTC(), from.UTC(), step, step,
		appID, dimension, updateIDs, from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("reading segmented health snapshots: %w", err)
	}
	defer rows.Close()
	return scanSegmentPoints(rows)
}

// segmentSnapshotsCover reports whether the counters can answer the window on
// their own.
//
// The counters are always younger than the events they summarise: a server
// installed today holds no bucket from last week, while device_health_events
// does. So a window reaching back further than the worker has been running is
// answered by rebuilding the grid, and that is not a migration path, it is
// where every deployment starts and where it stays for any window longer than
// its own uptime.
//
// Two ways to be unable to answer, and both have to be caught. The counters
// may not reach back to the start of the window, or they may reach back and be
// full of holes: a process down for two hours captured nothing for two hours,
// and a chart drawn from that shows a gap where the events would have shown a
// fleet. Checking only the start would have called that covered.
//
// Failing this check is never wrong, only slower, so it errs toward the grid:
// a young app with barely any buckets falls back, and rebuilding the grid for
// a young app is cheap precisely because it has few devices.
func (h *HealthHistory) segmentSnapshotsCover(ctx context.Context, appID string, from, to time.Time) (bool, error) {
	var earliest time.Time
	var captured uint64
	row := h.clickhouse.Conn.QueryRow(ctx, `
		SELECT min(bucket), toUInt64(count(DISTINCT bucket))
		FROM update_health_segment_snapshots
		WHERE app_id = ? AND bucket >= ? AND bucket <= ?`,
		appID, from.UTC().Add(-healthSegmentBucket), to.UTC())
	if err := row.Scan(&earliest, &captured); err != nil {
		return false, fmt.Errorf("checking segmented health coverage: %w", err)
	}
	// No rows at all comes back as the zero DateTime, which is before every
	// window and would claim a coverage this table does not have.
	if captured == 0 || earliest.IsZero() || earliest.Unix() <= 0 {
		return false, nil
	}
	// One bucket of slack at the start: the worker captures the bucket that
	// just closed, so a window opening mid-bucket is answered by the sample
	// that opens it.
	if earliest.After(from.Add(healthSegmentBucket)) {
		return false, nil
	}
	expected := uint64(to.Sub(from) / healthSegmentBucket)
	if expected == 0 {
		return true, nil
	}
	// A margin rather than an exact count: a capture that overran its slot, a
	// deploy, a rolling restart each cost a bucket or two, and sending an
	// otherwise complete window back to the grid over one missing sample would
	// give up the whole optimisation for a point nobody can see.
	return captured*100 >= expected*segmentCoverageMinPercent, nil
}
