// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"context"
	"errors"
	"expo-open-ota/ee/licensing"
	"expo-open-ota/internal/auditlog"
	"expo-open-ota/internal/services"
	"fmt"
	"net/netip"
	"strconv"
)

// ApiKeyRestrictions is the enterprise access restrictions attached to one
// API key: whether it may act on protected branches (false by default, an
// admin grants it explicitly) and the source networks it may be used from
// (empty = any address).
type ApiKeyRestrictions struct {
	ApiKeyID                   int64
	CanAccessProtectedBranches bool
	AllowedIps                 []netip.Prefix
}

// ApiKeyRestrictionRepository persists per-key restrictions and the branch
// protection flag. GetRestrictions and IsBranchProtected are the enforcement
// reads on the CLI request hot path.
type ApiKeyRestrictionRepository interface {
	GetRestrictionsByAppID(ctx context.Context, appID string) ([]ApiKeyRestrictions, error)
	SetRestrictions(ctx context.Context, appID string, apiKeyID int64, canAccessProtectedBranches bool, allowedIps []netip.Prefix) error
	GetRestrictions(ctx context.Context, apiKeyID int64) (ApiKeyRestrictions, error)
	SetBranchProtection(ctx context.Context, appID string, branchName string, protected bool) error
	IsBranchProtected(ctx context.Context, appID string, branchName string) (bool, error)
	// GetApiKeyName resolves the key's display name for the audit trail.
	GetApiKeyName(ctx context.Context, appID string, apiKeyID int64) (string, error)
}

var (
	ErrRequiresControlPlane = errors.New("api key restrictions are managed in the database: this deployment runs in stateless mode, which is community edition only")
	ErrRequiresValidLicense = errors.New("api key restrictions require an active enterprise license")
	ErrApiKeyNotFound       = errors.New("api key not found")
	ErrBranchNotFound       = errors.New("branch not found")
	ErrInvalidCidr          = errors.New("invalid IP or CIDR range")

	// Both wrap services.ErrCliAccessDenied so the community handlers can map
	// them to a 403 without knowing anything about this package.
	ErrIpNotAllowed    = fmt.Errorf("%w: this API key cannot be used from this IP address", services.ErrCliAccessDenied)
	ErrBranchProtected = fmt.Errorf("%w: this branch is protected and this API key is not allowed to act on protected branches", services.ErrCliAccessDenied)
)

// ApiKeyRestrictionService owns the management and the enforcement of per-key
// restrictions and branch protection. Mutations are license-gated (no valid
// license, no changes); reads are not, so the dashboard can always show what
// restrictions exist.
type ApiKeyRestrictionService struct {
	repo ApiKeyRestrictionRepository
	// licenseValid is the live licensing state; a field so same-package tests
	// can pin it without minting signed keys.
	licenseValid func() bool
	// onAuditEvent is the audit emission seam; nil means restriction changes
	// leave no events.
	onAuditEvent auditlog.RecordFunc
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *ApiKeyRestrictionService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// recordRestrictionEvent reports one restriction mutation, actor = the
// dashboard principal on the request context (both routes are
// permission-gated dashboard mutations).
func (s *ApiKeyRestrictionService) recordRestrictionEvent(ctx context.Context, action auditlog.Action, targetType string, targetID string, targetDisplay string, appID string, metadata map[string]any) {
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
		ActorType:     auditlog.ActorUser,
		ActorID:       actorID,
		ActorDisplay:  actorDisplay,
		Action:        action,
		TargetType:    targetType,
		TargetID:      targetID,
		TargetDisplay: targetDisplay,
		AppID:         appID,
		Outcome:       auditlog.OutcomeSuccess,
		Metadata:      metadata,
	})
}

// NewApiKeyRestrictionService accepts a nil repository (stateless mode);
// every management method then answers ErrRequiresControlPlane and the
// enforcement is a no-op.
func NewApiKeyRestrictionService(repo ApiKeyRestrictionRepository) *ApiKeyRestrictionService {
	return &ApiKeyRestrictionService{repo: repo, licenseValid: licensing.IsEnterprise}
}

func (s *ApiKeyRestrictionService) GetRestrictionsByApp(ctx context.Context, appID string) ([]ApiKeyRestrictions, error) {
	if s.repo == nil {
		return nil, ErrRequiresControlPlane
	}
	return s.repo.GetRestrictionsByAppID(ctx, appID)
}

// IsBranchProtected reports whether protection is currently ENFORCED on the
// branch, which is not the same question as whether the flag is set: the flag
// stays in the row when a license lapses, and this answers false then, exactly
// like AuthorizeCliRequest stops refusing API keys. A branch that does not
// exist yet is not protected.
//
// This is the read the dashboard's own protected-branch gate needs
// (rbac.RequirePermissionOnProtectedBranch), which is why it is a service
// method and not just a repository one.
func (s *ApiKeyRestrictionService) IsBranchProtected(ctx context.Context, appID string, branchName string) (bool, error) {
	if s.repo == nil || !s.licenseValid() {
		return false, nil
	}
	return s.repo.IsBranchProtected(ctx, appID, branchName)
}

// SetRestrictions replaces the restrictions of one API key. CIDR entries are
// normalized (bare addresses become /32 or /128, host bits are masked off)
// because the postgres cidr type rejects unmasked values.
func (s *ApiKeyRestrictionService) SetRestrictions(ctx context.Context, appID string, apiKeyID int64, canAccessProtectedBranches bool, cidrs []string) error {
	if s.repo == nil {
		return ErrRequiresControlPlane
	}
	if !s.licenseValid() {
		return ErrRequiresValidLicense
	}
	allowedIps, err := parseCidrs(cidrs)
	if err != nil {
		return err
	}
	if err := s.repo.SetRestrictions(ctx, appID, apiKeyID, canAccessProtectedBranches, allowedIps); err != nil {
		return err
	}
	// CIDRs are access policy, not secrets: the point of the entry is "who
	// widened this key's reach and when". Recorded in their NORMALIZED form,
	// the one actually persisted and enforced, not the raw input: parseCidrs
	// masks host bits, and the log must state the effective allow-list.
	normalizedCidrs := make([]string, len(allowedIps))
	for i, prefix := range allowedIps {
		normalizedCidrs[i] = prefix.String()
	}
	// Same convention as api_key.created/revoked: the entry names the key,
	// the numeric id stays the stable target id. Best-effort lookup.
	targetDisplay := strconv.FormatInt(apiKeyID, 10)
	if s.onAuditEvent != nil {
		if name, nameErr := s.repo.GetApiKeyName(ctx, appID, apiKeyID); nameErr == nil {
			targetDisplay = name
		}
	}
	s.recordRestrictionEvent(ctx, auditlog.ActionAPIKeyRestrictionsUpdated,
		"api_key", strconv.FormatInt(apiKeyID, 10), targetDisplay, appID,
		map[string]any{"can_access_protected_branches": canAccessProtectedBranches, "allowed_cidrs": normalizedCidrs})
	return nil
}

func (s *ApiKeyRestrictionService) SetBranchProtection(ctx context.Context, appID string, branchName string, protected bool) error {
	if s.repo == nil {
		return ErrRequiresControlPlane
	}
	if !s.licenseValid() {
		return ErrRequiresValidLicense
	}
	if err := s.repo.SetBranchProtection(ctx, appID, branchName, protected); err != nil {
		return err
	}
	s.recordRestrictionEvent(ctx, auditlog.ActionBranchProtectionUpdated,
		"branch", branchName, branchName, appID, map[string]any{"protected": protected})
	return nil
}

// AuthorizeCliRequest implements services.CliAccessPolicy: it enforces the
// authenticated key's restrictions on the CLI request path. Enforcement is an
// enterprise feature, so without a control plane or an active license nothing
// is enforced and every request passes (community behavior).
//
// branchName is empty only for requests whose ROUTE carries no branch, such as
// the local file upload; those go through the IP allowlist alone. Every route
// that names a branch passes it, reads included: the two app-scoped reads a
// publishing token may reach are branch-scoped, and letting them through
// unchecked made this restriction mean "cannot write" while it reads as
// "cannot touch". A branch that does
// not exist yet is not protected: publishing to a brand-new branch stays
// open, protecting it is an explicit admin action afterwards.
func (s *ApiKeyRestrictionService) AuthorizeCliRequest(ctx context.Context, appID string, apiKeyID int64, branchName string, clientIP netip.Addr) error {
	if s.repo == nil || !s.licenseValid() {
		return nil
	}
	restrictions, err := s.repo.GetRestrictions(ctx, apiKeyID)
	if err != nil {
		return err
	}
	if len(restrictions.AllowedIps) > 0 && !ipAllowed(clientIP, restrictions.AllowedIps) {
		return ipNotAllowedError(clientIP)
	}
	if branchName != "" && !restrictions.CanAccessProtectedBranches {
		protected, err := s.repo.IsBranchProtected(ctx, appID, branchName)
		if err != nil {
			return err
		}
		if protected {
			return ErrBranchProtected
		}
	}
	return nil
}

// ipNotAllowedError wraps ErrIpNotAllowed (and transitively
// services.ErrCliAccessDenied, so handlers still map it to a 403) while naming
// the address the server actually resolved for the caller. Surfacing it turns
// a blind "denied" into something an operator can debug against the allowlist,
// and an unresolved address points at the usual cause: a proxy in front of a
// server that does not trust forwarded headers.
func ipNotAllowedError(clientIP netip.Addr) error {
	if clientIP.IsValid() {
		return fmt.Errorf("%w (resolved client IP: %s)", ErrIpNotAllowed, clientIP)
	}
	return fmt.Errorf("%w (the server could not resolve your source IP; if it runs behind a proxy, set TRUST_PROXY_HEADERS)", ErrIpNotAllowed)
}
