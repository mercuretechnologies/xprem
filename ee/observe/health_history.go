// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/clickhouse"
	"expo-open-ota/internal/database/postgres"
	"expo-open-ota/internal/database/postgres/pgdb"

	"github.com/google/uuid"
)

const (
	healthOutboxBatchSize  = 500
	healthOutboxInterval   = time.Second
	healthSnapshotInterval = time.Minute
	healthDiscardInterval  = time.Minute
	// Floor between two change-driven snapshots. The outbox drains every
	// second and any drain that delivered something asks for a fresh
	// projection, so on a fleet that is never quiet the full-fleet aggregate
	// over PostgreSQL ran up to sixty times a minute to rewrite a bucket that
	// is truncated to the minute anyway. Ten seconds keeps a release visible
	// almost immediately while costing six aggregates a minute instead of
	// sixty; the minute heartbeat below is unaffected, it is the repair path.
	healthSnapshotFloor = 10 * time.Second
)

// HealthHistory projects PostgreSQL's durable health outbox and instant-T
// state into ClickHouse. PostgreSQL remains authoritative; every operation
// here is retryable and failures never affect manifest/telemetry requests.
type HealthHistory struct {
	postgres   *database.Engine
	clickhouse *clickhouse.Engine
}

func NewHealthHistory(postgresEngine *database.Engine, clickhouseEngine *clickhouse.Engine) *HealthHistory {
	return &HealthHistory{postgres: postgresEngine, clickhouse: clickhouseEngine}
}

// StartHealthOutboxDiscarder prevents the outbox from growing forever on a
// deployment that deliberately has no ClickHouse. Replica configuration is
// expected to be uniform: a mixed cluster where some replicas configure
// ClickHouse and others do not is unsupported by the telemetry pipeline too.
func StartHealthOutboxDiscarder(parent context.Context, postgresEngine *database.Engine) func() {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(healthDiscardInterval)
		defer ticker.Stop()
		for {
			if err := postgresEngine.DiscardDeviceHealthOutbox(ctx); err != nil && ctx.Err() == nil {
				log.Printf("observe: discarding disabled health-history outbox failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

// Start runs the projector until the returned cleanup is called. Outbox
// delivery is frequent for a responsive graph; absolute snapshots are one
// minute apart and make historical reads cheap.
func (h *HealthHistory) Start(parent context.Context) func() {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		h.run(ctx)
	}()
	go func() {
		defer wg.Done()
		h.runSegments(ctx)
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

// runSegments captures the segmented counters on their own goroutine and their
// own rhythm.
//
// Its own goroutine, because one capture rebuilds a grid over the whole fleet
// and is allowed minutes to do it. Sharing the loop above meant a slow capture
// held back outbox delivery and the per-minute snapshot, so a background job
// feeding a secondary chart could punch a hole in the headline one. The
// separate advisory lock was there to prevent exactly that and could not, since
// the two never ran concurrently to begin with.
//
// And deliberately NOT on the change trigger the unsplit snapshot follows. That
// trigger exists so a failing release shows up on the headline chart within
// seconds; the split is a breakdown a reader opens on purpose, and its finest
// bucket is five minutes.
func (h *HealthHistory) runSegments(ctx context.Context) {
	ticker := time.NewTicker(healthSegmentBucket)
	defer ticker.Stop()
	h.captureSegmentSnapshots(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.captureSegmentSnapshots(ctx)
		}
	}
}

func (h *HealthHistory) run(ctx context.Context) {
	outboxTicker := time.NewTicker(healthOutboxInterval)
	snapshotTicker := time.NewTicker(healthSnapshotInterval)
	defer outboxTicker.Stop()
	defer snapshotTicker.Stop()

	h.drainOutbox(ctx)
	h.captureSnapshots(ctx)
	lastCapture := time.Now()
	// A change waiting for the floor to elapse. Held rather than dropped: on a
	// quiet fleet the delivering drain is the only one there is, so forgetting
	// it meant the change waited for the next heartbeat instead of for the
	// floor, turning a second of staleness into up to a minute for exactly the
	// deployment where one failing device matters most.
	pending := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-outboxTicker.C:
			// Health changed: refresh the current minute, but no more often
			// than the floor. ClickHouse keeps one logical point per minute
			// via argMax, so anything faster rewrites the same bucket at the
			// price of another full-fleet read.
			pending = h.drainOutbox(ctx) || pending
			if pending && time.Since(lastCapture) >= healthSnapshotFloor {
				h.captureSnapshots(ctx)
				lastCapture = time.Now()
				pending = false
			}
		case <-snapshotTicker.C:
			// Heartbeat repairs a missed trigger and records quiet periods.
			h.captureSnapshots(ctx)
			lastCapture = time.Now()
			pending = false
		}
	}
}

// drainOutbox reports whether at least one event reached ClickHouse. The
// caller uses that as a cheap change notification for the snapshot projection.
func (h *HealthHistory) drainOutbox(ctx context.Context) bool {
	delivered := false
	for ctx.Err() == nil {
		count, err := h.deliverOutboxBatch(ctx)
		if err != nil {
			log.Printf("observe: health-history outbox delivery failed: %v", err)
			return delivered
		}
		delivered = delivered || count > 0
		if count < healthOutboxBatchSize {
			return delivered
		}
	}
	return delivered
}

// outboxAdvisoryLockID serializes outbox drainers across replicas (see
// migrationAdvisoryLockID in internal/database/postgres for the convention).
const outboxAdvisoryLockID = 745103622

// outboxSendTimeout bounds the ClickHouse insert. Nothing held it before, and
// the drain runs every second: an unreachable ClickHouse meant a delivery that
// never returned, on a loop.
const outboxSendTimeout = 15 * time.Second

// snapshotAdvisoryLockID elects one replica to take fleet snapshots. Separate
// from the outbox lock so a slow drain never stops a capture, and the other way
// round: they run on different cadences and neither waits on the other.
const snapshotAdvisoryLockID = 745103623

// lockOutbox elects one drainer across replicas.
func (h *HealthHistory) lockOutbox(ctx context.Context) (func(), bool, error) {
	return postgres.TryAdvisoryLock(ctx, h.postgres.DB, outboxAdvisoryLockID, "health outbox")
}

// deliverOutboxBatch moves one batch of events from PostgreSQL to ClickHouse.
//
// Read, send, delete, with NO transaction spanning the send. It used to be one
// transaction holding FOR UPDATE SKIP LOCKED row locks across the ClickHouse
// insert, which meant an unreachable ClickHouse pinned a connection and a
// transaction id for as long as it stayed unreachable, on a loop that fires
// every second. A transaction id that never advances is what stops vacuum from
// cleaning a table fed by every device state change.
//
// The cost of that shape is that delivery is at-least-once: a crash between
// the send and the delete replays the batch. That is what the destination was
// built for, device_health_events being a ReplacingMergeTree keyed on
// (app_id, outbox_id), so a replayed event collapses into the one already
// there rather than double-counting.
func (h *HealthHistory) deliverOutboxBatch(ctx context.Context) (int, error) {
	release, locked, err := h.lockOutbox(ctx)
	if err != nil {
		return 0, err
	}
	if !locked {
		// Another replica is draining. Not an error and not a miss: it is
		// delivering the same rows this one would have.
		return 0, nil
	}
	defer release()

	rows, err := h.postgres.Queries.ListDeviceHealthOutbox(ctx, healthOutboxBatchSize)
	if err != nil {
		return 0, fmt.Errorf("reading outbox rows: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}

	sendCtx, cancelSend := context.WithTimeout(ctx, outboxSendTimeout)
	defer cancelSend()
	batch, err := h.clickhouse.Conn.PrepareBatch(sendCtx, `INSERT INTO device_health_events
		(outbox_id, event_type, app_id, eas_client_id, update_id, previous_update_id,
		 failure_type, fatal_error, occurred_at,
		 branch, runtime_version, platform, os_name, os_version, device_model, country_code,
		 app_version)`)
	if err != nil {
		return 0, fmt.Errorf("preparing health event batch: %w", err)
	}
	defer batch.Close()
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		var previous any
		if row.PreviousUpdateID.Valid {
			previous = uuid.UUID(row.PreviousUpdateID.Bytes).String()
		}
		failureType := ""
		if row.FailureType != nil {
			failureType = *row.FailureType
		}
		if err := batch.Append(
			uint64(row.ID),
			row.EventType,
			uuid.UUID(row.AppID.Bytes).String(),
			uuid.UUID(row.EasClientID.Bytes).String(),
			uuid.UUID(row.UpdateID.Bytes).String(),
			previous,
			failureType,
			row.FatalError,
			row.OccurredAt.Time.UTC(),
			row.Branch,
			row.RuntimeVersion,
			row.Platform,
			row.OsName,
			row.OsVersion,
			row.DeviceModel,
			row.CountryCode,
			row.AppVersion,
		); err != nil {
			return 0, fmt.Errorf("appending health event: %w", err)
		}
		ids = append(ids, row.ID)
	}
	if err := batch.Send(); err != nil {
		return 0, fmt.Errorf("sending health event batch: %w", err)
	}
	// Delivered. Anything that goes wrong from here replays the batch, which
	// the destination absorbs.
	if err := h.postgres.Queries.DeleteDeviceHealthOutbox(ctx, ids); err != nil {
		return 0, fmt.Errorf("deleting delivered outbox rows: %w", err)
	}
	return len(rows), nil
}

func (h *HealthHistory) captureSnapshots(ctx context.Context) {
	// One replica at a time. ListCurrentUpdateHealthSnapshots aggregates the
	// whole device_identity table and then joins the failures of every update
	// it tracks, and every replica used to run it on its own timer: at ten
	// replicas that is ten identical full-fleet scans a minute, for one set of
	// numbers that would have been the same from any of them.
	release, locked, err := postgres.TryAdvisoryLock(ctx, h.postgres.DB, snapshotAdvisoryLockID, "health snapshot")
	if err != nil {
		log.Printf("observe: taking the health snapshot lock failed: %v", err)
		return
	}
	if !locked {
		// Another replica is capturing the same minute. Nothing to do and
		// nothing lost: the snapshot is a property of the fleet, not of the
		// replica that reads it.
		return
	}
	defer release()

	rows, err := h.postgres.ListCurrentUpdateHealthSnapshots(ctx)
	if err != nil {
		log.Printf("observe: health snapshot query failed: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	now := time.Now().UTC()
	bucket := now.Truncate(time.Minute)
	batch, err := h.clickhouse.Conn.PrepareBatch(ctx, `INSERT INTO update_health_snapshots
		(app_id, update_id, bucket, captured_at, role, devices_on_update,
		 successful_devices, faulty_devices, update_issues, runtime_issues)`)
	if err != nil {
		log.Printf("observe: preparing health snapshot batch failed: %v", err)
		return
	}
	defer batch.Close()
	for _, row := range rows {
		if err := appendSnapshot(batch, row, bucket, now); err != nil {
			log.Printf("observe: appending health snapshot failed: %v", err)
			return
		}
	}
	if err := batch.Send(); err != nil {
		log.Printf("observe: sending health snapshot batch failed: %v", err)
	}
}

type snapshotBatch interface {
	Append(v ...any) error
}

func appendSnapshot(batch snapshotBatch, row pgdb.ListCurrentUpdateHealthSnapshotsRow, bucket, capturedAt time.Time) error {
	return batch.Append(
		uuid.UUID(row.AppID.Bytes).String(),
		uuid.UUID(row.UpdateUuid.Bytes).String(),
		bucket,
		capturedAt,
		row.Role,
		uint64(max(row.DevicesOnUpdate, 0)),
		uint64(max(row.SuccessfulDevices, 0)),
		uint64(max(row.FaultyDevices, 0)),
		uint64(max(row.UpdateIssues, 0)),
		uint64(max(row.RuntimeIssues, 0)),
	)
}

// HealthHistoryPoint is one deduplicated minute of an update's historical
// health projection.
type HealthHistoryPoint struct {
	Timestamp         time.Time `json:"timestamp"`
	CapturedAt        time.Time `json:"capturedAt"`
	Role              string    `json:"role"`
	DevicesOnUpdate   uint64    `json:"devicesOnUpdate"`
	SuccessfulDevices uint64    `json:"successfulDevices"`
	FaultyDevices     uint64    `json:"faultyDevices"`
	UpdateIssues      uint64    `json:"updateIssues"`
	RuntimeIssues     uint64    `json:"runtimeIssues"`
	HealthPercent     *float64  `json:"healthPercent"`
}

func (h *HealthHistory) Read(
	ctx context.Context,
	appID string,
	updateIDs []string,
	from, to time.Time,
) (map[string][]HealthHistoryPoint, error) {
	rows, err := h.clickhouse.Conn.Query(ctx, `
		SELECT toString(update_id),
		       bucket,
		       max(captured_at),
		       argMax(role, captured_at),
		       argMax(devices_on_update, captured_at),
		       argMax(successful_devices, captured_at),
		       argMax(faulty_devices, captured_at),
		       argMax(update_issues, captured_at),
		       argMax(runtime_issues, captured_at)
		FROM update_health_snapshots
		WHERE app_id = ? AND toString(update_id) IN ? AND bucket >= ? AND bucket <= ?
		GROUP BY update_id, bucket
		ORDER BY update_id, bucket`, appID, updateIDs, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pointsByUpdate := make(map[string][]HealthHistoryPoint, len(updateIDs))
	for _, updateID := range updateIDs {
		pointsByUpdate[updateID] = []HealthHistoryPoint{}
	}
	for rows.Next() {
		var updateID string
		var point HealthHistoryPoint
		if err := rows.Scan(
			&updateID,
			&point.Timestamp,
			&point.CapturedAt,
			&point.Role,
			&point.DevicesOnUpdate,
			&point.SuccessfulDevices,
			&point.FaultyDevices,
			&point.UpdateIssues,
			&point.RuntimeIssues,
		); err != nil {
			return nil, err
		}
		attempts := point.SuccessfulDevices + point.FaultyDevices
		if attempts > 0 {
			percent := 100 * float64(point.SuccessfulDevices) / float64(attempts)
			point.HealthPercent = &percent
		}
		pointsByUpdate[updateID] = append(pointsByUpdate[updateID], point)
	}
	return pointsByUpdate, rows.Err()
}
