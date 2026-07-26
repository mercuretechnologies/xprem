// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"fmt"
	"time"
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
// telemetry rows, which is also their limit: a device that never sent
// telemetry still counts, under "unknown".
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
const maxHealthSegmentBuckets = 180

// The chart tells eight colours apart; past that a split stops informing. It
// is the LIMIT the query itself applies, so the trimming never has to happen
// on rows already pulled across the wire.
const maxHealthSegments = 8

// ReadBySegment answers "is this update failing on old Android phones", which
// the plain history cannot: its snapshots are pre-aggregated per update and
// carry nothing about the device. The raw events can, because
// device_health_events carries the eas_client_id of every adoption and every
// failure, and telemetry carries what that device is. This rebuilds the same
// curves per segment, on the fly.
//
// The cost is a device-by-bucket grid, so the bucket follows the window rather
// than the one-minute retention: an hour of a 24h window is 96 buckets, not
// 1440, and the shape is identical at this scale.
func (h *HealthHistory) ReadBySegment(
	ctx context.Context,
	appID string,
	updateIDs []string,
	dimension string,
	from, to time.Time,
) (map[string][]HealthSegmentPoint, error) {
	column, found := healthSegmentDimensions[dimension]
	if !found {
		return nil, errInvalidObserveFilter
	}
	if len(updateIDs) == 0 || !to.After(from) {
		return map[string][]HealthSegmentPoint{}, nil
	}

	step := int64((to.Sub(from) / maxHealthSegmentBuckets).Seconds())
	if step < 60 {
		step = 60
	}
	buckets := int64(to.Sub(from).Seconds())/step + 1

	// The dimension is an allowlisted column name, never caller input.
	//
	// The population is the health events, not the telemetry: a device that
	// never sent telemetry still adopted an update, and dropping it here would
	// silently answer for a subset of the fleet (a deployment that only has
	// manifest check-ins would get an empty chart). Telemetry only supplies the
	// segment, hence the LEFT JOIN and the "unknown" bucket.
	//
	// Faults join on the update as well as the device. Without that, a device
	// that crashed on an old update stays faulty for every update it runs
	// afterwards, and for native crashes that never clears: only runtime issues
	// ever emit a 'recovered' event.
	sql := sqlf(`
		WITH
		  device_segment AS (
		    SELECT eas_client_id, argMax(%s, timestamp) AS segment
		    FROM observe_metrics
		    WHERE app_id = ? AND timestamp >= ? AND timestamp <= ?
		    GROUP BY eas_client_id
		  ),
		  adoptions AS (
		    SELECT eas_client_id, update_id, occurred_at
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
		    SELECT DISTINCT eas_client_id FROM adoptions
		  ),
		  grid AS (
		    SELECT p.eas_client_id AS eas_client_id,
		           toDateTime(?) + toIntervalSecond(? * s.n) AS bucket
		    FROM population p
		    CROSS JOIN (SELECT arrayJoin(range(0, toUInt32(?))) AS n) AS s
		  ),
		  running AS (
		    SELECT g.bucket AS bucket, g.eas_client_id AS eas_client_id,
		           a.update_id AS update_id
		    FROM grid g
		    ASOF LEFT JOIN adoptions a
		      ON g.eas_client_id = a.eas_client_id AND g.bucket >= a.occurred_at
		  ),
		  failing AS (
		    SELECT r.bucket AS bucket, r.eas_client_id AS eas_client_id,
		           r.update_id AS update_id, f.faulty AS faulty
		    FROM running r
		    ASOF LEFT JOIN faults f
		      ON r.eas_client_id = f.eas_client_id
		         AND r.update_id = f.update_id
		         AND r.bucket >= f.occurred_at
		  ),
		  segment_counts AS (
		    SELECT run.bucket AS bucket, ifNull(seg.segment, '') AS segment,
		           count() AS devices, countIf(run.faulty = 1) AS faulty
		    FROM failing run
		    LEFT JOIN device_segment seg ON seg.eas_client_id = run.eas_client_id
		    WHERE toString(run.update_id) IN ?
		    GROUP BY bucket, segment
		  )
		SELECT bucket, segment, devices, faulty
		FROM segment_counts
		WHERE segment IN (
		  SELECT segment FROM segment_counts GROUP BY segment ORDER BY max(devices) DESC LIMIT ?
		)
		ORDER BY bucket`, column)

	rows, err := h.clickhouse.Conn.Query(ctx, sql,
		appID, from.UTC(), to.UTC(),
		appID, to.UTC(),
		appID, to.UTC(),
		from.UTC(), step, buckets,
		updateIDs,
		maxHealthSegments,
	)
	if err != nil {
		return nil, fmt.Errorf("reading segmented health history: %w", err)
	}
	defer rows.Close()

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

// unknownSegment is what a device with no telemetry lands under: the registry
// knows it exists and what it runs, never what it is.
const unknownSegment = "unknown"

// TrimSegments keeps the most populated segments. Everything else is noise on
// a chart that can only tell eight colours apart.
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
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && order[j].devices > order[j-1].devices; j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	trimmed := make(map[string][]HealthSegmentPoint, limit)
	for _, entry := range order[:limit] {
		trimmed[entry.segment] = bySegment[entry.segment]
	}
	return trimmed
}
