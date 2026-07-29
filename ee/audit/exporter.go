// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"expo-open-ota/config"
	"fmt"
	"log"
	"strconv"
	"time"
)

// ObjectPutter is the storage capability the archive exporter needs.
type ObjectPutter interface {
	PutObject(ctx context.Context, key string, body []byte) error
}

// exportBatchSize bounds one archive file; a var so tests can exercise the multi-batch loop cheaply.
var exportBatchSize = 1000

// exportLine is the NDJSON shape of one archived event, mirroring the HTTP API's field names.
type exportLine struct {
	Id            int64          `json:"id"`
	OccurredAt    string         `json:"occurredAt"`
	ActorType     string         `json:"actorType"`
	ActorId       string         `json:"actorId,omitempty"`
	ActorDisplay  string         `json:"actorDisplay"`
	Action        string         `json:"action"`
	TargetType    string         `json:"targetType"`
	TargetId      string         `json:"targetId"`
	TargetDisplay string         `json:"targetDisplay,omitempty"`
	AppId         string         `json:"appId,omitempty"`
	Outcome       string         `json:"outcome"`
	Ip            string         `json:"ip,omitempty"`
	UserAgent     string         `json:"userAgent,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

func exportLineFrom(event Event) exportLine {
	return exportLine{
		Id:            event.ID,
		OccurredAt:    event.OccurredAt.UTC().Format(time.RFC3339),
		ActorType:     string(event.ActorType),
		ActorId:       event.ActorID,
		ActorDisplay:  event.ActorDisplay,
		Action:        string(event.Action),
		TargetType:    event.TargetType,
		TargetId:      event.TargetID,
		TargetDisplay: event.TargetDisplay,
		AppId:         event.AppID,
		Outcome:       string(event.Outcome),
		Ip:            event.IP,
		UserAgent:     event.UserAgent,
		Metadata:      event.Metadata,
	}
}

// StartArchiveFromEnv reads the archive configuration from the environment and starts the exporter when enabled.
func (s *AuditService) StartArchiveFromEnv(ctx context.Context) error {
	if config.GetEnv("ARCHIVE_AUDIT_LOGS") != "true" {
		return nil
	}
	if s.repo == nil {
		return errors.New("ARCHIVE_AUDIT_LOGS requires the database control plane")
	}
	store, err := GetAuditLogsObjectStore()
	if err != nil {
		return err
	}
	intervalSeconds, intervalErr := strconv.Atoi(config.GetEnv("AUDIT_LOGS_EXPORT_INTERVAL_SECONDS"))
	if intervalErr != nil || intervalSeconds < 10 {
		log.Printf("⚠️  [AUDIT] Invalid AUDIT_LOGS_EXPORT_INTERVAL_SECONDS %q, using 300", config.GetEnv("AUDIT_LOGS_EXPORT_INTERVAL_SECONDS"))
		intervalSeconds = 300
	}
	s.startArchive(ctx, time.Duration(intervalSeconds)*time.Second, store)
	log.Printf("📦 [AUDIT] Archiving audit logs every %ds", intervalSeconds)
	return nil
}

// startArchive exports the audit log to the archive destination once at boot, then on the configured interval.
func (s *AuditService) startArchive(ctx context.Context, interval time.Duration, putter ObjectPutter) {
	if s.repo == nil || putter == nil {
		return
	}
	// Set before the goroutine starts so the retention purge sees it and spares unarchived rows.
	s.archiveEnabled = true
	go func() {
		s.runArchive(ctx, putter)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runArchive(ctx, putter)
			}
		}
	}()
}

func (s *AuditService) runArchive(ctx context.Context, putter ObjectPutter) {
	// Bounded per tick: a huge backlog resumes at the next tick instead of running unbounded.
	archiveCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	// Only one replica exports at a time; the others skip this tick.
	release, ok, err := s.repo.TryExportLock(archiveCtx)
	if err != nil {
		log.Printf("audit: archive export lock failed: %v", err)
		return
	}
	if !ok {
		return
	}
	defer release()
	for {
		exported, err := s.archiveNextBatch(archiveCtx, putter)
		if err != nil {
			log.Printf("audit: archive export failed: %v", err)
			return
		}
		if !exported {
			return
		}
	}
}

// archiveNextBatch exports one file and advances the cursor. It reports
// whether a full batch was written (meaning more rows may be waiting).
func (s *AuditService) archiveNextBatch(ctx context.Context, putter ObjectPutter) (bool, error) {
	cursor, err := s.repo.ExportCursor(ctx)
	if err != nil {
		return false, err
	}
	events, err := s.repo.ListAfter(ctx, cursor, exportBatchSize)
	if err != nil {
		return false, err
	}
	if len(events) == 0 {
		return false, nil
	}

	var body bytes.Buffer
	for _, event := range events {
		exportable := exportLineFrom(event)
		line, err := json.Marshal(exportable)
		if err != nil {
			// Metadata can fail to serialize; the event is still archived, without it.
			log.Printf("audit: archive dropped unserializable metadata on event %d: %v", event.ID, err)
			exportable.Metadata = nil
			if line, err = json.Marshal(exportable); err != nil {
				log.Printf("audit: archive skipped unserializable event %d: %v", event.ID, err)
				continue
			}
		}
		body.Write(line)
		body.WriteByte('\n')
	}

	firstDay := events[0].OccurredAt.UTC()
	lastID := events[len(events)-1].ID
	key := fmt.Sprintf("%04d/%02d/%02d/%d-%d.ndjson",
		firstDay.Year(), firstDay.Month(), firstDay.Day(), events[0].ID, lastID)
	if err := putter.PutObject(ctx, key, body.Bytes()); err != nil {
		return false, err
	}

	advanced, err := s.repo.AdvanceExportCursor(ctx, cursor, lastID)
	if err != nil {
		return false, err
	}
	if !advanced {
		// Another replica already exported this batch; our upload was an idempotent overwrite.
		return false, nil
	}
	return len(events) == exportBatchSize, nil
}
