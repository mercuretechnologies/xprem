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
// update then and how many had failed on it.
type HealthSegmentPoint struct {
	Timestamp         time.Time `json:"timestamp"`
	DevicesOnUpdate   uint64    `json:"devicesOnUpdate"`
	SuccessfulDevices uint64    `json:"successfulDevices"`
	FaultyDevices     uint64    `json:"faultyDevices"`
	HealthPercent     *float64  `json:"healthPercent"`
}

// healthSegmentDimensions are the dimensions a segmented health history can group by.
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

// maxHealthSegments is the most segments a chart shows; TrimSegments enforces it.
const maxHealthSegments = 8

// bucketCount cuts the window the same way the rest of the explorer does, via observeBucket.
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
		ctx,
		readCacheKey("health-segments", appID, updateIDs, dimension, from.UTC(), to.UTC()),
		func(ctx context.Context) (map[string][]HealthSegmentPoint, error) {
			return h.readBySegment(ctx, appID, updateIDs, dimension, from, to)
		})
}

// readBySegment answers from the precounted snapshots when the worker has
// covered the window, and rebuilds the grid live otherwise.
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

// gridSteps cuts the window into buckets, never finer than a minute (the events' retention floor).
func (h *HealthHistory) gridSteps(from, to time.Time) (buckets, step int64) {
	buckets = h.bucketCount(from, to)
	step = int64(to.Sub(from).Seconds()) / max(buckets-1, 1)
	if step < 60 {
		step = 60
		buckets = int64(to.Sub(from).Seconds())/step + 1
	}
	return buckets, step
}

// readSegmentGrid rebuilds the series from raw events via two ASOF joins;
// captureSegmentBucket precomputes this to a single instant.
func (h *HealthHistory) readSegmentGrid(
	ctx context.Context,
	appID string,
	updateIDs []string,
	dimension string,
	from, to time.Time,
	step, buckets int64,
) (map[string][]HealthSegmentPoint, error) {
	column := healthSegmentDimensions[dimension]

	// dimension is an allowlisted column name, never raw caller input.
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
