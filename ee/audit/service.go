// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package audit

import (
	"context"
	"errors"
	"log"
	"strconv"
	"time"
	"xprem/config"
	"xprem/ee/licensing"
	"xprem/internal/auditlog"
)

// Event, ActorType, Outcome and Action alias the vocabulary defined in internal/auditlog.
type (
	Event     = auditlog.Event
	ActorType = auditlog.ActorType
	Outcome   = auditlog.Outcome
	Action    = auditlog.Action
)

// ListFilters are the viewer's optional filters; nil means "any".
type ListFilters struct {
	ActorID *string
	Action  *string
	AppID   *string
	Outcome *string
	From    *time.Time
	To      *time.Time
}

// ListParams adds keyset pagination to the filters.
type ListParams struct {
	ListFilters
	BeforeID *int64
	Limit    int
}

const (
	DefaultPageSize = 50
	MaxPageSize     = 100
)

// AuditRepository persists and reads audit entries. Insert and PurgeBefore are its only writes.
type AuditRepository interface {
	Insert(ctx context.Context, event Event) (Event, error)
	List(ctx context.Context, params ListParams) ([]Event, error)
	Count(ctx context.Context, filters ListFilters) (int64, error)
	// exportedOnly restricts the purge to rows the archive exporter has already reached.
	PurgeBefore(ctx context.Context, cutoff time.Time, exportedOnly bool) (int64, error)
	ListAfter(ctx context.Context, afterID int64, limit int) ([]Event, error)
	ExportCursor(ctx context.Context) (int64, error)
	AdvanceExportCursor(ctx context.Context, from int64, to int64) (bool, error)
	// TryExportLock claims the cluster-wide exporter slot; ok is false when another replica holds it.
	TryExportLock(ctx context.Context) (release func(), ok bool, err error)
}

var (
	ErrRequiresControlPlane = errors.New("the audit log lives in the database: this deployment runs in stateless mode, which is community edition only")
)

// AuditService records and serves the enterprise audit trail. Recording requires a control
// plane and a valid license; reads only require the control plane.
type AuditService struct {
	repo         AuditRepository
	licenseValid func() bool
	// archiveEnabled makes the retention purge keep rows the exporter has not archived yet.
	archiveEnabled bool
}

// NewAuditService accepts a nil repository for stateless mode, where reads answer
// ErrRequiresControlPlane and Record no-ops.
func NewAuditService(repo AuditRepository) *AuditService {
	return &AuditService{repo: repo, licenseValid: licensing.IsEnterprise}
}

// Enabled reports whether events are being collected right now.
func (s *AuditService) Enabled() bool {
	return s.repo != nil && s.licenseValid()
}

// recordTimeout bounds the best-effort insert so a hung database can't pile up handler goroutines.
const recordTimeout = 5 * time.Second

// Record writes one event best-effort: a failed insert is logged and dropped rather than
// failing the request that emitted it. An empty ActorType or Outcome is stored as-is, never defaulted.
func (s *AuditService) Record(ctx context.Context, event Event) {
	if !s.Enabled() {
		return
	}
	meta := auditlog.MetaFromContext(ctx)
	if event.IP == "" {
		event.IP = meta.IP
	}
	if event.UserAgent == "" {
		event.UserAgent = meta.UserAgent
	}
	// Detached from the request context so the insert outlives a client disconnect.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	if _, err := s.repo.Insert(ctx, event); err != nil {
		log.Printf("audit: failed to record %q: %v", event.Action, err)
	}
}

// List returns one viewer page, newest first, and the cursor for the next one (nil on the last page).
func (s *AuditService) List(ctx context.Context, params ListParams) ([]Event, *int64, error) {
	if s.repo == nil {
		return nil, nil, ErrRequiresControlPlane
	}
	if params.Limit <= 0 {
		params.Limit = DefaultPageSize
	}
	if params.Limit > MaxPageSize {
		params.Limit = MaxPageSize
	}
	pageSize := params.Limit
	params.Limit = pageSize + 1
	events, err := s.repo.List(ctx, params)
	if err != nil {
		return nil, nil, err
	}
	if len(events) <= pageSize {
		return events, nil, nil
	}
	events = events[:pageSize]
	nextCursor := events[pageSize-1].ID
	return events, &nextCursor, nil
}

// Count is the filtered total shown next to the paginated list.
func (s *AuditService) Count(ctx context.Context, filters ListFilters) (int64, error) {
	if s.repo == nil {
		return 0, ErrRequiresControlPlane
	}
	return s.repo.Count(ctx, filters)
}

// PurgeOlderThan applies the retention window. It is not license gated: a lapsed license
// must not make entries outlive their retention. Rows the exporter has not archived yet survive.
func (s *AuditService) PurgeOlderThan(ctx context.Context, retention time.Duration) (int64, error) {
	if s.repo == nil {
		return 0, ErrRequiresControlPlane
	}
	return s.repo.PurgeBefore(ctx, time.Now().Add(-retention), s.archiveEnabled)
}

// StartRetentionPurgeFromEnv reads AUDIT_LOG_RETENTION_DAYS (default 550) and starts the daily purge.
func (s *AuditService) StartRetentionPurgeFromEnv(ctx context.Context) {
	retentionDays, err := strconv.Atoi(config.GetEnv("AUDIT_LOG_RETENTION_DAYS"))
	if err != nil || retentionDays < 1 {
		log.Printf("⚠️  [AUDIT] Invalid AUDIT_LOG_RETENTION_DAYS %q, using 550", config.GetEnv("AUDIT_LOG_RETENTION_DAYS"))
		retentionDays = 550
	}
	s.startRetentionPurge(ctx, time.Duration(retentionDays)*24*time.Hour)
}

// startRetentionPurge purges once at boot then daily. Concurrent replicas racing the same
// DELETE are harmless since rows are only deleted once.
func (s *AuditService) startRetentionPurge(ctx context.Context, retention time.Duration) {
	if s.repo == nil {
		return
	}
	go func() {
		s.runPurge(ctx, retention)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runPurge(ctx, retention)
			}
		}
	}()
}

func (s *AuditService) runPurge(ctx context.Context, retention time.Duration) {
	purgeCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	purged, err := s.PurgeOlderThan(purgeCtx, retention)
	if err != nil {
		log.Printf("audit: retention purge failed: %v", err)
		return
	}
	if purged > 0 {
		log.Printf("audit: retention purge removed %d events older than %s", purged, retention)
	}
}
