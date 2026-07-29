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
	// healthSegmentBucket is the fixed capture granularity for segment snapshots.
	healthSegmentBucket = 5 * time.Minute

	// segmentSnapshotAdvisoryLockID elects one replica to run a capture.
	segmentSnapshotAdvisoryLockID = 745103624

	// segmentCoverageMinPercent is the minimum bucket coverage before a window is served from the counters instead of the grid.
	segmentCoverageMinPercent = 95

	// segmentCaptureTimeout bounds a single snapshot capture.
	segmentCaptureTimeout = 4 * time.Minute
)

// captureSegmentSnapshotsAt counts, for the bucket that just closed, how many
// devices were on each update in each segment of each dimension, or does
// nothing if another replica is already capturing it.
func (h *HealthHistory) captureSegmentSnapshotsAt(ctx context.Context, bucket time.Time) {
	if h.clickhouse == nil {
		return
	}
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

	if err := h.captureSegmentBucket(ctx, bucket); err != nil {
		log.Printf("observe: capturing segmented health failed: %v", err)
	}
}

// captureSegmentBucket counts one bucket.
func (h *HealthHistory) captureSegmentBucket(ctx context.Context, bucket time.Time) error {
	return h.clickhouse.Conn.Exec(ctx, segmentCaptureSQL, bucket, bucket, bucket)
}

// segmentCaptureSQL counts devices per update per segment per dimension for a single instant, in one INSERT/SELECT statement.
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
    -- Faults join on update_id too, so a fault from an old update doesn't carry forward.
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
func (h *HealthHistory) readSegmentSnapshots(
	ctx context.Context,
	appID string,
	updateIDs []string,
	dimension string,
	from, to time.Time,
	step int64,
) (map[string][]HealthSegmentPoint, error) {
	rows, err := h.clickhouse.Conn.Query(ctx, `
		WITH mapped AS (
		  SELECT toDateTime(?) + toIntervalSecond(
		           greatest(0, toInt64(ceil(
		             (toInt64(toUInt32(bucket)) - toInt64(toUInt32(toDateTime(?)))) / ?
		           ))) * ?) AS t,
		         bucket, update_id, segment,
		         argMax(devices_on_update, captured_at) AS devices,
		         argMax(faulty_devices, captured_at) AS faulty
		  FROM (
		    -- A bucket is read from its newest capture only; a rewrite overwrites a cell but never removes one.
		    SELECT *, max(captured_at) OVER (PARTITION BY bucket) AS newest
		    FROM update_health_segment_snapshots
		    WHERE app_id = ? AND dimension = ? AND toString(update_id) IN ?
		      AND bucket >= ? AND bucket <= ?
		  )
		  WHERE captured_at = newest
		  GROUP BY t, bucket, update_id, segment
		)
		SELECT t, segment, sum(devices) AS devices, sum(faulty) AS faulty
		FROM (
		  SELECT t, segment, devices, faulty, bucket,
		         max(bucket) OVER (PARTITION BY t) AS answering
		  FROM mapped
		)
		WHERE bucket = answering
		GROUP BY t, segment
		ORDER BY t`,
		from.UTC(), from.UTC(), step, step,
		appID, dimension, updateIDs, from.UTC().Add(-healthSegmentBucket), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("reading segmented health snapshots: %w", err)
	}
	defer rows.Close()
	return scanSegmentPoints(rows)
}

// segmentSnapshotsCover reports whether the counters cover enough of the
// window to answer it, falling back to the grid otherwise.
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
	// No rows at all comes back as the zero DateTime, not a real earliest bucket.
	if captured == 0 || earliest.IsZero() || earliest.Unix() <= 0 {
		return false, nil
	}
	if earliest.After(from.Add(healthSegmentBucket)) {
		return false, nil
	}
	expected := uint64(to.Sub(from) / healthSegmentBucket)
	if expected == 0 {
		return true, nil
	}
	return captured*100 >= expected*segmentCoverageMinPercent, nil
}
