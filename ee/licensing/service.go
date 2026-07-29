// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"context"
	"errors"
	"expo-open-ota/internal/auditlog"
	"expo-open-ota/internal/services"
	"log"
	"time"
)

// StoredLicense is the persisted license key row, plus when it was last
// written (i.e. when the key was activated on this deployment).
type StoredLicense struct {
	Key       string
	UpdatedAt time.Time
}

// LicenseRepository is the enterprise_license table, a single row holding the
// key. It has no bucket implementation on purpose: license activation only
// exists on the control plane, stateless deployments run community edition.
type LicenseRepository interface {
	// GetLicense returns nil (no error) when no key has been stored yet.
	GetLicense(ctx context.Context) (*StoredLicense, error)
	UpsertLicense(ctx context.Context, key string) (StoredLicense, error)
	DeleteLicense(ctx context.Context) error
}

// ErrLicenseRequiresControlPlane is answered by every method when the service
// was built without a repository (stateless mode), mapped to a 400 by the
// handler.
var ErrLicenseRequiresControlPlane = errors.New("the enterprise license is managed in the database: this deployment runs in stateless mode, which is community edition only")

// LicenseStatus describes the stored key and what it unlocks. License is set
// whenever the key's signature verifies, including for an expired license, so
// the dashboard can show *when* it expired. Err carries the reason the key is
// not usable (malformed, bad signature, expired); a valid active license has
// License != nil and Err == nil.
type LicenseStatus struct {
	HasKey      bool
	License     *License
	ActivatedAt time.Time
	Err         error
}

func (s LicenseStatus) Valid() bool {
	return s.License != nil && s.Err == nil
}

// LicenseService owns the stored enterprise license key: it verifies keys
// before persisting them and keeps the process-wide activation state
// (licensing.IsEnterprise) in sync with the database.
type LicenseService struct {
	repo LicenseRepository
	// onAuditEvent is the audit emission seam; nil means license changes
	// leave no events. Only the admin-called Activate/Remove emit, the boot
	// load and the sync poll are state convergence, not actions.
	onAuditEvent auditlog.RecordFunc
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *LicenseService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// recordLicenseEvent reports an admin license action, actor = the admin
// principal on the request context (both routes are admin-gated).
func (s *LicenseService) recordLicenseEvent(ctx context.Context, action auditlog.Action, metadata map[string]any) {
	if s.onAuditEvent == nil {
		return
	}
	actorID, actorDisplay := "", ""
	if principal := services.PrincipalFromContext(ctx); principal != nil {
		actorID, actorDisplay = principal.UserId, principal.Email
		if actorDisplay == "" {
			actorDisplay = principal.UserId
		}
	}
	s.onAuditEvent(ctx, auditlog.Event{
		ActorType:    auditlog.ActorUser,
		ActorID:      actorID,
		ActorDisplay: actorDisplay,
		Action:       action,
		TargetType:   "license",
		TargetID:     "license",
		Outcome:      auditlog.OutcomeSuccess,
		Metadata:     metadata,
	})
}

// NewLicenseService accepts a nil repository (stateless mode); every method
// then answers ErrLicenseRequiresControlPlane.
func NewLicenseService(repo LicenseRepository) *LicenseService {
	return &LicenseService{repo: repo}
}

func (s *LicenseService) statusOf(stored *StoredLicense) LicenseStatus {
	if stored == nil {
		return LicenseStatus{}
	}
	status := LicenseStatus{HasKey: true, ActivatedAt: stored.UpdatedAt}
	license, err := Parse(stored.Key)
	if err != nil {
		status.Err = err
		return status
	}
	status.License = license
	if license.Expired() {
		status.Err = ErrExpired
	}
	return status
}

// Status reports on the stored key without touching the activation state.
func (s *LicenseService) Status(ctx context.Context) (LicenseStatus, error) {
	if s.repo == nil {
		return LicenseStatus{}, ErrLicenseRequiresControlPlane
	}
	stored, err := s.repo.GetLicense(ctx)
	if err != nil {
		return LicenseStatus{}, err
	}
	return s.statusOf(stored), nil
}

// Activate verifies a key, persists it as the deployment's license and makes
// it the active license for this process. An invalid or expired key is
// rejected without overwriting the stored one.
func (s *LicenseService) Activate(ctx context.Context, key string) (LicenseStatus, error) {
	if s.repo == nil {
		return LicenseStatus{}, ErrLicenseRequiresControlPlane
	}
	license, err := Parse(key)
	if err != nil {
		return LicenseStatus{}, err
	}
	if license.Expired() {
		return LicenseStatus{}, ErrExpired
	}
	stored, err := s.repo.UpsertLicense(ctx, key)
	if err != nil {
		return LicenseStatus{}, err
	}
	if _, err := Activate(key); err != nil {
		// Unreachable in practice: the key just parsed and is not expired.
		return LicenseStatus{}, err
	}
	metadata := map[string]any{"license_id": license.LicenseID}
	if license.Expiry != nil {
		metadata["expires_at"] = license.Expiry.Format(time.RFC3339)
	}
	s.recordLicenseEvent(ctx, auditlog.ActionLicenseActivated, metadata)
	return LicenseStatus{HasKey: true, License: license, ActivatedAt: stored.UpdatedAt}, nil
}

// Remove deletes the stored key and drops back to community edition.
func (s *LicenseService) Remove(ctx context.Context) error {
	if s.repo == nil {
		return ErrLicenseRequiresControlPlane
	}
	if err := s.repo.DeleteLicense(ctx); err != nil {
		return err
	}
	// Emitted before Deactivate: dropping to community closes the recorder's
	// license gate, and the removal must be the last entry it lets through.
	s.recordLicenseEvent(ctx, auditlog.ActionLicenseRemoved, nil)
	Deactivate()
	return nil
}

// ActivateFromStore loads the stored key at boot and activates it when valid.
// A missing, invalid or expired key means community edition, never a boot
// failure, so it only returns infrastructure errors (database unreachable).
// Steady-state changes are picked up by StartSync afterwards.
func (s *LicenseService) ActivateFromStore(ctx context.Context) error {
	if s.repo == nil {
		return nil
	}
	stored, err := s.repo.GetLicense(ctx)
	if err != nil {
		return err
	}
	if stored == nil {
		log.Println("🏘️  [LICENSE] No enterprise license key stored, running community edition")
		return nil
	}
	license, err := Activate(stored.Key)
	if err != nil {
		log.Printf("⚠️  [LICENSE] Stored enterprise license key is not usable (%v), running community edition", err)
		return nil
	}
	if license.Expiry != nil {
		log.Printf("🏢 [LICENSE] Enterprise edition enabled (license %s, expires %s)", license.LicenseID, license.Expiry.UTC().Format(time.RFC3339))
	} else {
		log.Printf("🏢 [LICENSE] Enterprise edition enabled (license %s, perpetual)", license.LicenseID)
	}
	return nil
}

// StartSync keeps IsEnterprise() honest on replicas that did not serve the
// dashboard request that changed the license: the in-process activation state
// is otherwise only written at boot and by the replica handling the
// activation/removal, so the others would drift until their next restart.
// The reconciliation is a primary-key read on a one-row table, so a short
// interval costs nothing. No-op in stateless mode; runs until ctx is
// cancelled.
func (s *LicenseService) StartSync(ctx context.Context, interval time.Duration) {
	if s.repo == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.syncFromStore(ctx)
			}
		}
	}()
}

func equalExpiry(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// syncFromStore reconciles the process-wide activation state with the stored
// key. Re-activation is unconditional when the stored key verifies, one
// Ed25519 check per interval is negligible and it also propagates renewals
// (same license id, new expiry), but transitions are only logged when the
// active license actually changed, so the loop stays silent in steady state.
func (s *LicenseService) syncFromStore(ctx context.Context) {
	stored, err := s.repo.GetLicense(ctx)
	if err != nil {
		log.Printf("⚠️  [LICENSE] Could not re-read the enterprise license from the database: %v", err)
		return
	}
	previous := Current()
	if stored == nil {
		Deactivate()
		if previous != nil {
			log.Println("🏘️  [LICENSE] Enterprise license removed from the database, dropping to community edition")
		}
		return
	}
	license, err := Activate(stored.Key)
	if err != nil {
		// Activate leaves the previous state untouched on failure, so an
		// unusable stored key must drop the in-memory license explicitly.
		Deactivate()
		if previous != nil {
			log.Printf("⚠️  [LICENSE] Stored enterprise license key is no longer usable (%v), dropping to community edition", err)
		}
		return
	}
	if previous == nil || previous.LicenseID != license.LicenseID || !equalExpiry(previous.Expiry, license.Expiry) {
		log.Printf("🏢 [LICENSE] Enterprise license %s synced from the database", license.LicenseID)
	}
}
