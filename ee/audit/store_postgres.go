// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
	"xprem/internal/database"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/store"

	"github.com/jackc/pgx/v5/pgtype"
)

type PostgresAuditStore struct {
	engine *database.Engine
}

func NewPostgresAuditStore(engine *database.Engine) *PostgresAuditStore {
	return &PostgresAuditStore{engine: engine}
}

// marshalMetadata returns "{}" instead of SQL NULL when metadata is empty or fails to serialize.
func marshalMetadata(action Action, metadata map[string]any) []byte {
	if len(metadata) == 0 {
		return []byte("{}")
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		log.Printf("audit: dropping unserializable metadata on %q: %v", action, err)
		return []byte("{}")
	}
	return payload
}

func eventFromRow(row pgdb.AuditLogEvent) Event {
	event := Event{
		ID:            row.ID,
		OccurredAt:    row.OccurredAt.Time,
		ActorType:     ActorType(row.ActorType),
		ActorID:       row.ActorID,
		ActorDisplay:  row.ActorDisplay,
		Action:        Action(row.Action),
		TargetType:    row.TargetType,
		TargetID:      row.TargetID,
		TargetDisplay: row.TargetDisplay,
		Outcome:       Outcome(row.Outcome),
		IP:            row.Ip,
		UserAgent:     row.UserAgent,
	}
	if row.AppID != nil {
		event.AppID = *row.AppID
	}
	// A row whose metadata fails to parse still lists, just without it.
	if len(row.Metadata) > 0 {
		var metadata map[string]any
		if err := json.Unmarshal(row.Metadata, &metadata); err == nil && len(metadata) > 0 {
			event.Metadata = metadata
		}
	}
	return event
}

func (s *PostgresAuditStore) Insert(ctx context.Context, event Event) (Event, error) {
	var appID *string
	if event.AppID != "" {
		appID = &event.AppID
	}
	row, err := s.engine.Queries.InsertAuditLogEvent(ctx, pgdb.InsertAuditLogEventParams{
		ActorType:     string(event.ActorType),
		ActorID:       event.ActorID,
		ActorDisplay:  event.ActorDisplay,
		Action:        string(event.Action),
		TargetType:    event.TargetType,
		TargetID:      event.TargetID,
		TargetDisplay: event.TargetDisplay,
		AppID:         appID,
		Outcome:       string(event.Outcome),
		Ip:            event.IP,
		UserAgent:     event.UserAgent,
		Metadata:      marshalMetadata(event.Action, event.Metadata),
	})
	if err != nil {
		return Event{}, fmt.Errorf("failed to insert audit event in database: %w", err)
	}
	return eventFromRow(row), nil
}

func (s *PostgresAuditStore) List(ctx context.Context, params ListParams) ([]Event, error) {
	rows, err := s.engine.Queries.ListAuditLogEvents(ctx, pgdb.ListAuditLogEventsParams{
		ActorID:      params.ActorID,
		Action:       params.Action,
		AppID:        params.AppID,
		Outcome:      params.Outcome,
		OccurredFrom: store.ToPgTimestamptz(params.From),
		OccurredTo:   store.ToPgTimestamptz(params.To),
		BeforeID:     params.BeforeID,
		RowLimit:     int32(params.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list audit events from database: %w", err)
	}
	events := make([]Event, len(rows))
	for i, row := range rows {
		events[i] = eventFromRow(row)
	}
	return events, nil
}

func (s *PostgresAuditStore) PurgeBefore(ctx context.Context, cutoff time.Time, exportedOnly bool) (int64, error) {
	pgCutoff := pgtype.Timestamptz{Time: cutoff, Valid: true}
	if exportedOnly {
		commandTag, err := s.engine.Queries.PurgeExportedAuditLogEventsBefore(ctx, pgCutoff)
		if err != nil {
			return 0, fmt.Errorf("failed to purge exported audit events from database: %w", err)
		}
		return commandTag.RowsAffected(), nil
	}
	commandTag, err := s.engine.Queries.PurgeAuditLogEventsBefore(ctx, pgCutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to purge audit events from database: %w", err)
	}
	return commandTag.RowsAffected(), nil
}

func (s *PostgresAuditStore) ListAfter(ctx context.Context, afterID int64, limit int) ([]Event, error) {
	rows, err := s.engine.Queries.ListAuditLogEventsAfter(ctx, pgdb.ListAuditLogEventsAfterParams{
		ID:    afterID,
		Limit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list audit events for export from database: %w", err)
	}
	events := make([]Event, len(rows))
	for i, row := range rows {
		events[i] = eventFromRow(row)
	}
	return events, nil
}

func (s *PostgresAuditStore) ExportCursor(ctx context.Context) (int64, error) {
	cursor, err := s.engine.Queries.GetAuditExportCursor(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to read the audit export cursor from database: %w", err)
	}
	return cursor, nil
}

// TryExportLock claims the "one exporter at a time" advisory lock.
func (s *PostgresAuditStore) TryExportLock(ctx context.Context) (func(), bool, error) {
	return postgres.TryAdvisoryLock(ctx, s.engine.DB, postgres.AuditExportLockID, "audit export")
}

func (s *PostgresAuditStore) AdvanceExportCursor(ctx context.Context, from int64, to int64) (bool, error) {
	commandTag, err := s.engine.Queries.AdvanceAuditExportCursor(ctx, pgdb.AdvanceAuditExportCursorParams{
		LastExportedID:   from,
		LastExportedID_2: to,
	})
	if err != nil {
		return false, fmt.Errorf("failed to advance the audit export cursor in database: %w", err)
	}
	return commandTag.RowsAffected() == 1, nil
}

func (s *PostgresAuditStore) Count(ctx context.Context, filters ListFilters) (int64, error) {
	count, err := s.engine.Queries.CountAuditLogEvents(ctx, pgdb.CountAuditLogEventsParams{
		ActorID:      filters.ActorID,
		Action:       filters.Action,
		AppID:        filters.AppID,
		Outcome:      filters.Outcome,
		OccurredFrom: store.ToPgTimestamptz(filters.From),
		OccurredTo:   store.ToPgTimestamptz(filters.To),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to count audit events in database: %w", err)
	}
	return count, nil
}
