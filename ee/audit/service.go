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

// The audit vocabulary (event shape, action catalog, emission seam) lives in
// the community-side leaf package internal/auditlog, so the services emitting
// events never import this one. This package is the machinery behind the
// seam: the license-gated recorder and the Postgres store. The aliases keep
// its own files and tests on first-name terms with the domain types.
type (
	Event     = auditlog.Event
	ActorType = auditlog.ActorType
	Outcome   = auditlog.Outcome
	Action    = auditlog.Action
)

// ListFilters are the viewer's optional filters; nil means "any". Outcome is
// the security lens: "everything that failed or was refused, whatever the
// action".
type ListFilters struct {
	ActorID *string
	Action  *string
	AppID   *string
	Outcome *string
	From    *time.Time
	To      *time.Time
}

// ListParams adds keyset pagination to the filters. BeforeID is the cursor
// (nil on the first page); Limit is clamped to [1, MaxPageSize] by the
// service, 0 meaning DefaultPageSize.
type ListParams struct {
	ListFilters
	BeforeID *int64
	Limit    int
}

const (
	DefaultPageSize = 50
	MaxPageSize     = 100
)

// AuditRepository persists and reads audit entries. Insert is the only write
// besides PurgeBefore: the log is append-only by construction, the retention
// purge is its single sanctioned exception.
type AuditRepository interface {
	Insert(ctx context.Context, event Event) (Event, error)
	List(ctx context.Context, params ListParams) ([]Event, error)
	Count(ctx context.Context, filters ListFilters) (int64, error)
	// exportedOnly keeps expired rows the archive exporter has not reached
	// yet: set while archiving is enabled, so nothing is deleted unarchived.
	PurgeBefore(ctx context.Context, cutoff time.Time, exportedOnly bool) (int64, error)
	// The archive exporter's cursor and batch read (see exporter.go).
	ListAfter(ctx context.Context, afterID int64, limit int) ([]Event, error)
	ExportCursor(ctx context.Context) (int64, error)
	AdvanceExportCursor(ctx context.Context, from int64, to int64) (bool, error)
	// TryExportLock claims the cluster-wide exporter slot: ok=false means
	// another replica is archiving right now, skip the tick. release gives
	// the slot back. Purely an upload-dedup optimization: the cursor CAS
	// alone keeps concurrent exporters correct.
	TryExportLock(ctx context.Context) (release func(), ok bool, err error)
}

var (
	ErrRequiresControlPlane = errors.New("the audit log lives in the database: this deployment runs in stateless mode, which is community edition only")
)

// AuditService records and serves the enterprise audit trail. Recording is
// hard-gated on Enabled(): without a control plane and a currently valid
// license, Record is a silent no-op and nothing is ever collected. Reads only
// need the control plane, so a deployment whose license lapsed keeps read
// access to what was collected while it was licensed.
type AuditService struct {
	repo         AuditRepository
	licenseValid func() bool
	// archiveEnabled is set by startArchive (before any purge goroutine
	// starts, see the wiring order) and makes the retention purge keep
	// expired rows the exporter has not archived yet.
	archiveEnabled bool
}

// NewAuditService accepts a nil repository (stateless mode); reads then answer
// ErrRequiresControlPlane and Record no-ops. The license gate defaults to
// licensing.IsEnterprise and is imported directly ON PURPOSE: it must live in
// EE code so bypassing it means editing an EE-licensed file (a license
// violation), not swapping a func passed in from the MIT composition root
// (which anyone may legally replace with `func() bool { return true }`).
// Tests flip the licenseValid field instead of a signed key.
func NewAuditService(repo AuditRepository) *AuditService {
	return &AuditService{repo: repo, licenseValid: licensing.IsEnterprise}
}

// Enabled reports whether events are being collected right now.
func (s *AuditService) Enabled() bool {
	return s.repo != nil && s.licenseValid()
}

// recordTimeout bounds the best-effort insert: a hung database must degrade
// to lost audit entries (logged), never to handler goroutines piling up.
const recordTimeout = 5 * time.Second

// Record writes one event, best-effort: a failed insert logs and is dropped
// rather than failing the user-facing request that emitted it. Its method
// value is what the wiring hands to every emitting surface as their
// auditlog.RecordFunc, so call sites stay unconditional: the enterprise gate
// lives here. An empty ActorType or Outcome is stored as-is, never defaulted:
// on a security log an incomplete entry must name its call-site bug, a
// fabricated "system"/"success" would hide it.
func (s *AuditService) Record(ctx context.Context, event Event) {
	if !s.Enabled() {
		return
	}
	// Call sites never carry the request's network facts themselves: the
	// RequestMetaMiddleware stamped them on the context.
	meta := auditlog.MetaFromContext(ctx)
	if event.IP == "" {
		event.IP = meta.IP
	}
	if event.UserAgent == "" {
		event.UserAgent = meta.UserAgent
	}
	// The entry must land even when the client disconnects right after the
	// mutation: the insert outlives the request context's cancellation, but
	// never recordTimeout.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	if _, err := s.repo.Insert(ctx, event); err != nil {
		log.Printf("audit: failed to record %q: %v", event.Action, err)
	}
}

// List returns one viewer page, newest first, and the cursor for the next one
// (nil when this page is the last). It fetches one extra row to answer "is
// there more" without a second query.
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

// PurgeOlderThan applies the retention window. Deliberately not license
// gated: retention is a promise about collected data, and a lapsed license
// must not make entries outlive it. While archiving is enabled, expired rows
// the exporter has not archived yet survive until it catches up: "purged rows
// live on in the archive" must hold even against a large export backlog.
func (s *AuditService) PurgeOlderThan(ctx context.Context, retention time.Duration) (int64, error) {
	if s.repo == nil {
		return 0, ErrRequiresControlPlane
	}
	return s.repo.PurgeBefore(ctx, time.Now().Add(-retention), s.archiveEnabled)
}

// StartRetentionPurgeFromEnv reads AUDIT_LOG_RETENTION_DAYS (falling back to
// 550, roughly eighteen months) and starts the daily purge. Not license
// gated, so a lapsed license cannot make entries outlive the retention
// promise; a stateless deployment declines inside startRetentionPurge.
func (s *AuditService) StartRetentionPurgeFromEnv(ctx context.Context) {
	retentionDays, err := strconv.Atoi(config.GetEnv("AUDIT_LOG_RETENTION_DAYS"))
	if err != nil || retentionDays < 1 {
		log.Printf("⚠️  [AUDIT] Invalid AUDIT_LOG_RETENTION_DAYS %q, using 550", config.GetEnv("AUDIT_LOG_RETENTION_DAYS"))
		retentionDays = 550
	}
	s.startRetentionPurge(ctx, time.Duration(retentionDays)*24*time.Hour)
}

// startRetentionPurge purges once at boot then daily (see licensing.StartSync
// for the pattern). Multiple replicas racing the same DELETE are harmless:
// rows are only deleted once.
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
