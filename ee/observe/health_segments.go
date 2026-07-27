// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// HealthSegmentPoint is one bucket of one segment: how many devices ran the
// update then, and how many of them had failed on it.
type HealthSegmentPoint struct {
	Timestamp         time.Time `json:"timestamp"`
	DevicesOnUpdate   uint64    `json:"devicesOnUpdate"`
	SuccessfulDevices uint64    `json:"successfulDevices"`
	FaultyDevices     uint64    `json:"faultyDevices"`
	HealthPercent     *float64  `json:"healthPercent"`
}

// Dimensions a segmented health history can group by. They are read from the
// health events themselves, where they were frozen at delivery
// (20260725180000_health_event_dimensions.sql and its app_version follow-up),
// so a bucket is labelled with what the device was THEN. Rows delivered before
// those migrations carry empty strings, which read as "unknown".
var healthSegmentDimensions = map[string]sqlFragment{
	"deviceModel":    "device_model",
	"osVersion":      "os_version",
	"osName":         "os_name",
	"country":        "country_code",
	"appVersion":     "app_version",
	"platform":       "platform",
	"runtimeVersion": "runtime_version",
	"branch":         "branch",
}

func IsHealthSegmentDimension(name string) bool {
	_, found := healthSegmentDimensions[name]
	return found
}

// Grid size guard: devices times buckets is what this query materializes, and
// a fleet of a million devices at one-minute buckets is not a chart, it is an
// outage. Buckets are capped, and the caller's window does the rest.

// The chart tells eight colours apart; past that a split stops informing.
// Applied by TrimSegments on the rows already read, deliberately NOT as a
// LIMIT in the query: selecting the top segments in SQL means a second
// reference to the aggregate, and ClickHouse substitutes a CTE rather than
// materializing it, so the whole grid and both ASOF joins were computed twice
// (measured: six scans of device_health_events instead of three, and eighteen
// times the memory) to trim a result the caller was trimming anyway.
const maxHealthSegments = 8

// ReadBySegment answers "is this update failing on old Android phones", which
// the plain history cannot: its snapshots are pre-aggregated per update and
// carry nothing about the device. The raw events can, because
// device_health_events carries the eas_client_id of every adoption and every
// failure along with what the device was at that moment. This rebuilds the
// same curves per segment, on the fly.
//
// The cost is a device-by-bucket grid, so the bucket follows the window rather
// than the one-minute retention: an hour of a 24h window is 96 buckets, not
// 1440, and the shape is identical at this scale. The grid is also the reason
// the population is narrowed to devices that adopted one of the requested
// updates: a device that never ran one cannot survive the final filter anyway,
// and building its row per bucket first is how a fleet of a million turns into
// 181 million intermediate rows for a chart about two updates.
// bucketCount cuts the window exactly the way the rest of the explorer cuts
// it, through observeBucket, so two series over one window are sampled the
// same way. Deliberately independent of how large the fleet is: deriving the
// step from the population would have given two apps different granularity for
// the same window, which reads as a difference in the data rather than in the
// sampling, and the size problem belongs to caching and pre-aggregation rather
// than to silently coarsening one chart.
func (h *HealthHistory) bucketCount(from, to time.Time) int64 {
	window := to.Sub(from)
	step := int64(observeBucket(window).Seconds())
	buckets := int64(window.Seconds())/step + 1
	return buckets
}

func (h *HealthHistory) ReadBySegment(
	ctx context.Context,
	appID string,
	updateIDs []string,
	dimension string,
	from, to time.Time,
) (map[string][]HealthSegmentPoint, error) {
	return cachedRead(
		readCacheKey("health-segments", appID, updateIDs, dimension, from.UTC(), to.UTC()),
		func() (map[string][]HealthSegmentPoint, error) {
			return h.readBySegment(ctx, appID, updateIDs, dimension, from, to)
		})
}

// readBySegment is the read itself, behind the cache above. It answers from the
// precounted snapshots when the worker has been running long enough to cover
// the window, and rebuilds the grid live when it has not.
//
// The fallback is what a deployment sees for the first hours after an upgrade,
// and what it keeps seeing for any window reaching back past the day the
// counters started. It is the same answer, paid for on the spot.
func (h *HealthHistory) readBySegment(
	ctx context.Context,
	appID string,
	updateIDs []string,
	dimension string,
	from, to time.Time,
) (map[string][]HealthSegmentPoint, error) {
	if !IsHealthSegmentDimension(dimension) {
		return nil, errInvalidObserveFilter
	}
	if len(updateIDs) == 0 || !to.After(from) {
		return map[string][]HealthSegmentPoint{}, nil
	}
	buckets, step := h.gridSteps(from, to)

	// A coverage check that fails is not a reason to fail the read: the grid
	// answers the same question, more slowly.
	covered, err := h.segmentSnapshotsCover(ctx, appID, from, to)
	if err != nil {
		log.Printf("observe: falling back to the live segmented grid: %v", err)
	}
	if covered {
		return h.readSegmentSnapshots(ctx, appID, updateIDs, dimension, from, to, step)
	}
	return h.readSegmentGrid(ctx, appID, updateIDs, dimension, from, to, step, buckets)
}

// gridSteps cuts the window the way every other series on the page is cut, and
// never finer than a minute, which is the retention of the events themselves.
func (h *HealthHistory) gridSteps(from, to time.Time) (buckets, step int64) {
	buckets = h.bucketCount(from, to)
	step = int64(to.Sub(from).Seconds()) / max(buckets-1, 1)
	if step < 60 {
		step = 60
		buckets = int64(to.Sub(from).Seconds())/step + 1
	}
	return buckets, step
}

// readSegmentGrid rebuilds the series from the raw events: one row per device
// per bucket, walked by two ASOF joins. The cost is that product rather than
// the rows it reads, which on a million devices stayed at 3.3 million while
// the query took several seconds. This is what captureSegmentBucket collapses
// to a single instant so a viewer never pays it.
func (h *HealthHistory) readSegmentGrid(
	ctx context.Context,
	appID string,
	updateIDs []string,
	dimension string,
	from, to time.Time,
	step, buckets int64,
) (map[string][]HealthSegmentPoint, error) {
	column := healthSegmentDimensions[dimension]

	// The dimension is an allowlisted column name, never caller input.
	//
	// The segment rides on the adoption event, so the ASOF that already decides
	// which update a device was running at a bucket decides its segment too:
	// one source, no extra join, and the label is the device's state at that
	// point rather than its state today. Reading it from telemetry instead
	// meant one value per device for the whole window, which relabelled a
	// device's entire history the moment it upgraded, and left every segment
	// "unknown" on a deployment with no telemetry at all.
	//
	// Faults join on the update as well as the device. Without that, a device
	// that crashed on an old update stays faulty for every update it runs
	// afterwards, and for native crashes that never clears: only runtime issues
	// ever emit a 'recovered' event.
	sql := sqlf(`
		WITH
		  adoptions AS (
		    SELECT eas_client_id, update_id, occurred_at, %s AS segment
		    FROM device_health_events
		    WHERE app_id = ? AND event_type IN ('first_seen', 'switched')
		      AND occurred_at <= ?
		  ),
		  faults AS (
		    SELECT eas_client_id, update_id, occurred_at,
		           event_type = 'failure' AS faulty
		    FROM device_health_events
		    WHERE app_id = ? AND event_type IN ('failure', 'recovered')
		      AND occurred_at <= ?
		  ),
		  population AS (
		    SELECT DISTINCT eas_client_id FROM adoptions WHERE toString(update_id) IN ?
		  ),
		  grid AS (
		    SELECT p.eas_client_id AS eas_client_id,
		           toDateTime(?) + toIntervalSecond(? * s.n) AS bucket
		    FROM population p
		    CROSS JOIN (SELECT arrayJoin(range(0, toUInt32(?))) AS n) AS s
		  ),
		  running AS (
		    SELECT g.bucket AS bucket, g.eas_client_id AS eas_client_id,
		           a.update_id AS update_id, a.segment AS segment
		    FROM grid g
		    ASOF LEFT JOIN adoptions a
		      ON g.eas_client_id = a.eas_client_id AND g.bucket >= a.occurred_at
		  ),
		  failing AS (
		    SELECT r.bucket AS bucket, r.eas_client_id AS eas_client_id,
		           r.update_id AS update_id, r.segment AS segment, f.faulty AS faulty
		    FROM running r
		    ASOF LEFT JOIN faults f
		      ON r.eas_client_id = f.eas_client_id
		         AND r.update_id = f.update_id
		         AND r.bucket >= f.occurred_at
		  ),
		  segment_counts AS (
		    SELECT run.bucket AS bucket, run.segment AS segment,
		           count() AS devices, countIf(run.faulty = 1) AS faulty
		    FROM failing run
		    -- The population only says a device ran a requested update at some
		    -- point; this says it was running one at THIS bucket.
		    WHERE toString(run.update_id) IN ?
		    GROUP BY bucket, segment
		  )
		SELECT bucket, segment, devices, faulty
		FROM segment_counts
		ORDER BY bucket`, column)

	rows, err := h.clickhouse.Conn.Query(ctx, sql,
		appID, to.UTC(),
		appID, to.UTC(),
		updateIDs,
		from.UTC(), step, buckets,
		updateIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("reading segmented health history: %w", err)
	}
	defer rows.Close()
	return scanSegmentPoints(rows)
}

// scanSegmentPoints turns (bucket, segment, devices, faulty) rows into the
// series the chart draws. Shared by the live grid and by the precounted
// snapshots so the two can never disagree on how a count becomes a percentage.
func scanSegmentPoints(rows driver.Rows) (map[string][]HealthSegmentPoint, error) {
	bySegment := make(map[string][]HealthSegmentPoint)
	for rows.Next() {
		var segment string
		var point HealthSegmentPoint
		var devices, faulty uint64
		if err := rows.Scan(&point.Timestamp, &segment, &devices, &faulty); err != nil {
			return nil, err
		}
		// A device counted as faulty is still running the update it failed on
		// in the adoption sense, so successes are the complement. Note this is
		// the population currently on the update, where the pre-aggregated
		// snapshots also carry devices that have since left it: the two curves
		// answer the same question over slightly different fleets.
		point.DevicesOnUpdate = devices
		point.FaultyDevices = faulty
		if faulty > devices {
			faulty = devices
		}
		point.SuccessfulDevices = devices - faulty
		if devices > 0 {
			percent := 100 * float64(point.SuccessfulDevices) / float64(devices)
			point.HealthPercent = &percent
		}
		if segment == "" {
			segment = unknownSegment
		}
		bySegment[segment] = append(bySegment[segment], point)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return bySegment, nil
}

// unknownSegment is what an event carrying no value for the dimension lands
// under. Two causes, and the label must not pick one: the registry never
// learned the value (a device that sends no telemetry reports no hardware), or
// the event predates the migration that added the column, since neither
// 20260725180000_health_event_dimensions.sql nor its app_version follow-up
// backfilled. The second dominates right after an upgrade and fades as the
// window stops reaching back past it.
const unknownSegment = "unknown"

// TrimSegments keeps the most populated segments. Everything else is noise on
// a chart that can only tell eight colours apart. This is the only place the
// cut happens, so ties break on the segment name: ranking off a map alone
// would let two equally populated segments swap places between refreshes, and
// a series appearing and disappearing on a chart nobody touched reads as data
// moving.
func TrimSegments(bySegment map[string][]HealthSegmentPoint, limit int) map[string][]HealthSegmentPoint {
	if len(bySegment) <= limit {
		return bySegment
	}
	type ranked struct {
		segment string
		devices uint64
	}
	order := make([]ranked, 0, len(bySegment))
	for segment, points := range bySegment {
		var peak uint64
		for _, point := range points {
			if point.DevicesOnUpdate > peak {
				peak = point.DevicesOnUpdate
			}
		}
		order = append(order, ranked{segment: segment, devices: peak})
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].devices != order[j].devices {
			return order[i].devices > order[j].devices
		}
		return order[i].segment < order[j].segment
	})
	trimmed := make(map[string][]HealthSegmentPoint, limit)
	for _, entry := range order[:limit] {
		trimmed[entry.segment] = bySegment[entry.segment]
	}
	return trimmed
}
