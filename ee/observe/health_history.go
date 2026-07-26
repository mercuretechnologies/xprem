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
	"expo-open-ota/internal/database/postgres/pgdb"

	"github.com/google/uuid"
)

const (
	healthOutboxBatchSize  = 500
	healthOutboxInterval   = time.Second
	healthSnapshotInterval = time.Minute
	healthDiscardInterval  = time.Minute
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
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.run(ctx)
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

func (h *HealthHistory) run(ctx context.Context) {
	outboxTicker := time.NewTicker(healthOutboxInterval)
	snapshotTicker := time.NewTicker(healthSnapshotInterval)
	defer outboxTicker.Stop()
	defer snapshotTicker.Stop()

	h.drainOutbox(ctx)
	h.captureSnapshots(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-outboxTicker.C:
			if h.drainOutbox(ctx) {
				// Health changed: refresh the current minute immediately.
				// ClickHouse keeps one logical point per minute via argMax.
				h.captureSnapshots(ctx)
			}
		case <-snapshotTicker.C:
			// Heartbeat repairs a missed trigger and records quiet periods.
			h.captureSnapshots(ctx)
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

func (h *HealthHistory) deliverOutboxBatch(ctx context.Context) (int, error) {
	tx, err := h.postgres.DB.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning outbox transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	queries := h.postgres.Queries.WithTx(tx)
	rows, err := queries.ListDeviceHealthOutbox(ctx, healthOutboxBatchSize)
	if err != nil {
		return 0, fmt.Errorf("claiming outbox rows: %w", err)
	}
	if len(rows) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return 0, fmt.Errorf("committing empty outbox transaction: %w", err)
		}
		return 0, nil
	}

	batch, err := h.clickhouse.Conn.PrepareBatch(ctx, `INSERT INTO device_health_events
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
	if err := queries.DeleteDeviceHealthOutbox(ctx, ids); err != nil {
		return 0, fmt.Errorf("deleting delivered outbox rows: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing outbox delivery: %w", err)
	}
	return len(rows), nil
}

func (h *HealthHistory) captureSnapshots(ctx context.Context) {
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
