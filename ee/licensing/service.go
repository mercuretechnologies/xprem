// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
	"xprem/internal/auditlog"
	"xprem/internal/cache"
	"xprem/internal/services"
	"xprem/internal/version"
)

// GracePeriod is how long a failing license keeps enterprise features on.
const GracePeriod = 7 * 24 * time.Hour

const (
	validateInterval = 15 * time.Minute
	validateLockKey  = "license-validate-lock"
	// Shorter than the interval so the next tick can always reclaim the lock.
	validateLockTTLSeconds = 840
)

// StoredLicense is the persisted activation, without the activation secret
// (see LicenseRepository.GetActivationSecret).
type StoredLicense struct {
	Key                 string
	License             License
	ActivatedAt         time.Time
	LastValidatedAt     *time.Time
	ValidationFailedAt  *time.Time
	ValidationErrorCode string
}

// LicenseRepository is the enterprise_license table, a single row holding the
// activation.
type LicenseRepository interface {
	// GetLicense returns nil (no error) when no license has been attached.
	GetLicense(ctx context.Context) (*StoredLicense, error)
	GetActivationSecret(ctx context.Context) (string, error)
	SaveActivation(ctx context.Context, key string, activationSecret string, license License) (StoredLicense, error)
	MarkValidated(ctx context.Context, license License) (StoredLicense, error)
	// MarkValidationFailed keeps the first failure timestamp on repeats.
	MarkValidationFailed(ctx context.Context, errorCode string) (StoredLicense, error)
	DeleteLicense(ctx context.Context) error
}

// ErrLicenseRequiresControlPlane is answered by every method when the service
// was built without a repository (stateless mode).
var ErrLicenseRequiresControlPlane = errors.New("the enterprise license is managed in the database: this deployment runs in stateless mode, which is community edition only")

// LicenseStatus describes the stored activation and what it unlocks.
type LicenseStatus struct {
	HasKey              bool
	License             *License
	ActivatedAt         time.Time
	LastValidatedAt     *time.Time
	ValidationFailedAt  *time.Time
	ValidationErrorCode string
}

// GraceDeadline is when the failing license drops, nil while validation holds.
func (s LicenseStatus) GraceDeadline() *time.Time {
	if s.ValidationFailedAt == nil {
		return nil
	}
	deadline := s.ValidationFailedAt.Add(GracePeriod)
	return &deadline
}

// Suspended reports whether the grace window is exhausted.
func (s LicenseStatus) Suspended() bool {
	deadline := s.GraceDeadline()
	return deadline != nil && time.Now().After(*deadline)
}

// Valid reports whether enterprise features are on.
func (s LicenseStatus) Valid() bool {
	return s.HasKey && !s.Suspended()
}

// LicenseService owns the stored activation and keeps the process-wide
// activation state in sync with the database.
type LicenseService struct {
	repo         LicenseRepository
	client       *Client
	instanceId   string
	baseUrl      string
	onAuditEvent auditlog.RecordFunc
}

// NewLicenseService accepts a nil repository (stateless mode); every method
// then answers ErrLicenseRequiresControlPlane.
func NewLicenseService(repo LicenseRepository, client *Client, instanceId string, baseUrl string) *LicenseService {
	// The server matches baseUrl by exact string equality.
	return &LicenseService{repo: repo, client: client, instanceId: instanceId, baseUrl: strings.TrimSuffix(baseUrl, "/")}
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *LicenseService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

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

func (s *LicenseService) recordSuspendedEvent(ctx context.Context, errorCode string) {
	if s.onAuditEvent == nil {
		return
	}
	s.onAuditEvent(ctx, auditlog.Event{
		ActorType:    auditlog.ActorSystem,
		ActorID:      "license-validation",
		ActorDisplay: "license validation",
		Action:       auditlog.ActionLicenseSuspended,
		TargetType:   "license",
		TargetID:     "license",
		Outcome:      auditlog.OutcomeSuccess,
		Metadata:     map[string]any{"error_code": errorCode},
	})
}

func (s *LicenseService) statusOf(stored *StoredLicense) LicenseStatus {
	if stored == nil {
		return LicenseStatus{}
	}
	license := stored.License
	return LicenseStatus{
		HasKey:              true,
		License:             &license,
		ActivatedAt:         stored.ActivatedAt,
		LastValidatedAt:     stored.LastValidatedAt,
		ValidationFailedAt:  stored.ValidationFailedAt,
		ValidationErrorCode: stored.ValidationErrorCode,
	}
}

func applyStatus(status LicenseStatus) {
	if status.Valid() {
		Activate(*status.License)
	} else {
		Deactivate()
	}
}

func (s *LicenseService) keyParams(key string) KeyParams {
	return KeyParams{
		InstanceId: s.instanceId,
		LicenseKey: key,
		BaseUrl:    s.baseUrl,
		Version:    version.Version,
	}
}

var ErrInstanceIdUnavailable = errors.New("licensing: the server instance id is unavailable, see the boot logs")

// Status reports on the stored activation without touching anything.
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

func planSupported(l License) bool {
	return strings.EqualFold(l.PlanCode, PlanEnterprise)
}

// Check asks the license server whether a key is usable, without consuming it.
func (s *LicenseService) Check(ctx context.Context, key string) (CheckResult, error) {
	if s.repo == nil {
		return CheckResult{}, ErrLicenseRequiresControlPlane
	}
	if s.instanceId == "" {
		return CheckResult{}, ErrInstanceIdUnavailable
	}
	result, err := s.client.Check(ctx, s.keyParams(key))
	if err != nil {
		return CheckResult{}, err
	}
	if result.Valid && !planSupported(*result.License) {
		return CheckResult{ErrorCode: CodePlanNotSupported}, nil
	}
	return result, nil
}

// Attach checks the key, then consumes it on the license server and persists
// the activation.
func (s *LicenseService) Attach(ctx context.Context, key string) (LicenseStatus, error) {
	if s.repo == nil {
		return LicenseStatus{}, ErrLicenseRequiresControlPlane
	}
	if s.instanceId == "" {
		return LicenseStatus{}, ErrInstanceIdUnavailable
	}
	check, err := s.Check(ctx, key)
	if err != nil {
		return LicenseStatus{}, err
	}
	if !check.Valid {
		return LicenseStatus{}, &DecisionError{Code: check.ErrorCode}
	}
	activation, err := s.client.Attach(ctx, s.keyParams(key))
	if err != nil {
		return LicenseStatus{}, err
	}
	// The attach consumed the single-use key server-side; a cancelled request
	// must not lose the only copy of the activation secret.
	stored, err := s.repo.SaveActivation(context.WithoutCancel(ctx), key, activation.ActivationSecret, activation.License)
	if err != nil {
		return LicenseStatus{}, err
	}
	Activate(stored.License)
	s.recordLicenseEvent(ctx, auditlog.ActionLicenseActivated, map[string]any{
		"org":       stored.License.OrgName,
		"plan_code": stored.License.PlanCode,
	})
	return s.statusOf(&stored), nil
}

// Remove deletes the stored activation and drops back to community edition.
func (s *LicenseService) Remove(ctx context.Context) error {
	if s.repo == nil {
		return ErrLicenseRequiresControlPlane
	}
	if err := s.repo.DeleteLicense(ctx); err != nil {
		return err
	}
	// Emitted before Deactivate, which closes the recorder's license gate.
	s.recordLicenseEvent(ctx, auditlog.ActionLicenseRemoved, nil)
	Deactivate()
	return nil
}

// ActivateFromStore loads the stored activation at boot; it only returns
// infrastructure errors, a missing or suspended activation means community
// edition.
func (s *LicenseService) ActivateFromStore(ctx context.Context) error {
	if s.repo == nil {
		return nil
	}
	stored, err := s.repo.GetLicense(ctx)
	if err != nil {
		return err
	}
	status := s.statusOf(stored)
	applyStatus(status)
	switch {
	case stored == nil:
		log.Println("🏘️  [LICENSE] No enterprise license attached, running community edition")
	case status.Suspended():
		log.Printf("⚠️  [LICENSE] The enterprise license has not been verifiable since %s (%s) and the grace period is over, running community edition. Contact support@xprem.dev.", stored.ValidationFailedAt.UTC().Format(time.RFC3339), stored.ValidationErrorCode)
	case status.ValidationFailedAt != nil:
		log.Printf("⚠️  [LICENSE] Enterprise edition enabled, but the license could not be verified since %s (%s); it drops on %s. Contact support@xprem.dev.", stored.ValidationFailedAt.UTC().Format(time.RFC3339), stored.ValidationErrorCode, status.GraceDeadline().UTC().Format(time.RFC3339))
	default:
		log.Printf("🏢 [LICENSE] Enterprise edition enabled (%s, %s plan)", stored.License.OrgName, stored.License.PlanCode)
	}
	return nil
}

// ValidateNow re-checks the stored activation against the license server and
// persists the outcome.
func (s *LicenseService) ValidateNow(ctx context.Context) {
	if s.repo == nil {
		return
	}
	stored, err := s.repo.GetLicense(ctx)
	if err != nil {
		log.Printf("⚠️  [LICENSE] Could not read the enterprise license from the database: %v", err)
		return
	}
	if stored == nil {
		return
	}
	// Local problems must not start the grace window.
	if s.instanceId == "" {
		log.Println("⚠️  [LICENSE] Skipping license validation: the server instance id is unavailable, see the boot logs")
		return
	}
	activationSecret, err := s.repo.GetActivationSecret(ctx)
	if err != nil {
		log.Printf("⚠️  [LICENSE] Skipping license validation: %v", err)
		return
	}
	wasFailing := stored.ValidationFailedAt != nil
	license, err := s.client.Validate(ctx, ValidateParams{
		InstanceId:       s.instanceId,
		ActivationSecret: activationSecret,
		BaseUrl:          s.baseUrl,
	})
	if err == nil && !planSupported(*license) {
		err = &DecisionError{Code: CodePlanNotSupported}
	}
	if err == nil {
		updated, repoErr := s.repo.MarkValidated(ctx, *license)
		if repoErr != nil {
			log.Printf("⚠️  [LICENSE] Could not persist the license validation: %v", repoErr)
			return
		}
		applyStatus(s.statusOf(&updated))
		if wasFailing {
			log.Println("🏢 [LICENSE] Enterprise license verification recovered")
		}
		return
	}

	// A cancelled context is a shutdown, not a license decision.
	if ctx.Err() != nil {
		return
	}
	code := CodeServerUnreachable
	if errors.Is(err, ErrServerRejected) {
		code = CodeServerRejected
	}
	var refusal *DecisionError
	if errors.As(err, &refusal) {
		code = refusal.Code
	}
	updated, repoErr := s.repo.MarkValidationFailed(ctx, code)
	if repoErr != nil {
		log.Printf("⚠️  [LICENSE] License validation failed (%v) and the failure could not be persisted: %v", err, repoErr)
		return
	}
	status := s.statusOf(&updated)
	if status.Suspended() {
		if IsEnterprise() {
			log.Printf("🚨 [LICENSE] The enterprise license could not be verified for %d days (%s), dropping to community edition. Contact support@xprem.dev.", int(GracePeriod.Hours()/24), code)
			// Before Deactivate: the gate must still be open.
			s.recordSuspendedEvent(ctx, code)
		}
		Deactivate()
		return
	}
	if deadline := status.GraceDeadline(); deadline != nil {
		log.Printf("⚠️  [LICENSE] Could not verify the enterprise license (%s); enterprise features stay on until %s. Contact support@xprem.dev if this persists.", code, deadline.UTC().Format(time.RFC3339))
	}
}

// StartValidationLoop re-validates now and then every 15 minutes until ctx is
// cancelled; the cache lock keeps it to one validation per interval.
func (s *LicenseService) StartValidationLoop(ctx context.Context) {
	if s.repo == nil {
		return
	}
	go func() {
		s.validateWithLock(ctx)
		ticker := time.NewTicker(validateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.validateWithLock(ctx)
			}
		}
	}()
}

func (s *LicenseService) validateWithLock(ctx context.Context) {
	locked, err := cache.GetCache().TryLock(validateLockKey, validateLockTTLSeconds)
	if err != nil {
		// A broken lock backend must not silently stop validation; a duplicate
		// run is harmless.
		log.Printf("⚠️  [LICENSE] Could not take the validation lock, validating anyway: %v", err)
	} else if !locked {
		return
	}
	s.ValidateNow(ctx)
}

// StartSync reconciles the in-process activation state with the stored row
// until ctx is cancelled.
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

func (s *LicenseService) syncFromStore(ctx context.Context) {
	stored, err := s.repo.GetLicense(ctx)
	if err != nil {
		log.Printf("⚠️  [LICENSE] Could not re-read the enterprise license from the database: %v", err)
		return
	}
	previous := Current()
	status := s.statusOf(stored)
	// Emitted before applyStatus: deactivating closes the recorder's license
	// gate. The validation loop only observes the deadline every 15 minutes,
	// so the suspension is usually first seen here.
	if previous != nil && stored != nil && status.Suspended() {
		s.recordSuspendedEvent(ctx, stored.ValidationErrorCode)
	}
	applyStatus(status)
	if previous == nil && status.Valid() {
		log.Printf("🏢 [LICENSE] Enterprise license synced from the database (%s)", status.License.OrgName)
	}
	if previous != nil && !status.Valid() {
		if stored == nil {
			log.Println("🏘️  [LICENSE] Enterprise license removed from the database, dropping to community edition")
		} else {
			log.Printf("🚨 [LICENSE] Enterprise license suspended (%s), dropping to community edition. Contact support@xprem.dev.", stored.ValidationErrorCode)
		}
	}
}
