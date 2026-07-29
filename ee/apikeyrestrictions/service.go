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

// ApiKeyAccess is everything one API key is allowed to do: the branches it
// reaches and what it may do there, whether it may publish to a branch that
// does not exist yet, and the source networks it may be used from. An empty
// BranchRules means every branch of the app, which is what a key has always
// been able to do and all a community deployment ever sees.
type ApiKeyAccess struct {
	ApiKeyID            int64
	AllowedIps          []netip.Prefix
	AllowBranchCreation bool
	BranchRules         []BranchRule
}

// CliRequest is one authenticated CLI request, in the terms the access
// decision is made in. It is built by the router, which is the only layer
// holding the route, its branch and what it does at the same time.
type CliRequest struct {
	AppID    string
	APIKeyID int64
	Branch   string
	Action   Action
	ClientIP netip.Addr
}

// ApiKeyAccessRepository persists per-key access. GetAccess and BranchExists
// are the enforcement reads on the CLI request hot path.
type ApiKeyAccessRepository interface {
	GetAccessByAppID(ctx context.Context, appID string) ([]ApiKeyAccess, error)
	GetAccess(ctx context.Context, apiKeyID int64) (ApiKeyAccess, error)
	SetAccess(ctx context.Context, appID string, access ApiKeyAccess) error
	BranchExists(ctx context.Context, appID string, branchName string) (bool, error)
	// GetApiKeyName resolves the key's display name for the audit trail.
	GetApiKeyName(ctx context.Context, appID string, apiKeyID int64) (string, error)
}

var (
	ErrRequiresControlPlane = errors.New("api key access rules are managed in the database: this deployment runs in stateless mode, which is community edition only")
	ErrRequiresValidLicense = errors.New("api key access rules require an active enterprise license")
	ErrApiKeyNotFound       = errors.New("api key not found")
	ErrInvalidCidr          = errors.New("invalid IP or CIDR range")

	// Both wrap services.ErrCliAccessDenied so the community handlers can map
	// them to a 403 without knowing anything about this package.
	ErrIpNotAllowed = fmt.Errorf("%w: this API key cannot be used from this IP address", services.ErrCliAccessDenied)
)

// deniedError names the branch and the action, because the alternative is a
// CI job that fails with "access denied" and an operator who has to guess
// which of the two ends of the rule is wrong. The holder of the key is the
// team that owns the app, not a stranger.
func deniedError(action Action, branchName string) error {
	return fmt.Errorf("%w: this API key is not allowed to %s on branch %q", services.ErrCliAccessDenied, action, branchName)
}

// unjudged turns a repository failure into "could not verify", so a database
// outage reaches the CLI as a 500 rather than as "your key is invalid". A key
// that is simply gone is not an outage: that one keeps its 401.
func unjudged(err error) error {
	if errors.Is(err, ErrApiKeyNotFound) {
		return err
	}
	return fmt.Errorf("%w: %w", services.ErrCliAuthUnavailable, err)
}

func branchCreationDeniedError(branchName string) error {
	return fmt.Errorf("%w: branch %q does not exist and this API key is not allowed to create branches", services.ErrCliAccessDenied, branchName)
}

// ApiKeyAccessService owns the management and the enforcement of per-key
// access. Mutations are license-gated (no valid license, no changes); reads
// are not, so the dashboard can always show what a key is allowed to do.
type ApiKeyAccessService struct {
	repo ApiKeyAccessRepository
	// licenseValid is the live licensing state; a field so same-package tests
	// can pin it without minting signed keys.
	licenseValid func() bool
	// onAuditEvent is the audit emission seam; nil means access changes leave
	// no events.
	onAuditEvent auditlog.RecordFunc
}

// NewApiKeyAccessService accepts a nil repository (stateless mode); every
// management method then answers ErrRequiresControlPlane and the enforcement
// is a no-op.
func NewApiKeyAccessService(repo ApiKeyAccessRepository) *ApiKeyAccessService {
	return &ApiKeyAccessService{repo: repo, licenseValid: licensing.IsEnterprise}
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *ApiKeyAccessService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

func (s *ApiKeyAccessService) GetAccessByApp(ctx context.Context, appID string) ([]ApiKeyAccess, error) {
	if s.repo == nil {
		return nil, ErrRequiresControlPlane
	}
	return s.repo.GetAccessByAppID(ctx, appID)
}

// SetAccess replaces what one API key is allowed to do. CIDR entries are
// normalized (bare addresses become /32 or /128, host bits are masked off)
// because the postgres cidr type rejects unmasked values; rules are validated
// and reordered by NormalizeBranchRules.
func (s *ApiKeyAccessService) SetAccess(ctx context.Context, appID string, apiKeyID int64, allowBranchCreation bool, rules []BranchRule, cidrs []string) error {
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
	normalizedRules, err := NormalizeBranchRules(rules)
	if err != nil {
		return err
	}
	access := ApiKeyAccess{
		ApiKeyID:            apiKeyID,
		AllowedIps:          allowedIps,
		AllowBranchCreation: allowBranchCreation,
		BranchRules:         normalizedRules,
	}
	if err := s.repo.SetAccess(ctx, appID, access); err != nil {
		return err
	}
	// Access policy is not secret: the point of the entry is "who widened this
	// key's reach and when". CIDRs are recorded in their NORMALIZED form, the
	// one actually enforced, and rules in the form the dashboard shows.
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
	s.recordAccessEvent(ctx, auditlog.ActionAPIKeyRestrictionsUpdated,
		"api_key", strconv.FormatInt(apiKeyID, 10), targetDisplay, appID,
		map[string]any{
			"branch_rules":          describeBranchRules(normalizedRules),
			"allow_branch_creation": allowBranchCreation,
			"allowed_cidrs":         normalizedCidrs,
		})
	return nil
}

// Authorize is the enforcement point for an authenticated CLI request.
// Enforcement is an enterprise feature, so without a control plane or an
// active license nothing is enforced and every request passes.
//
// The order of the checks is the order of the answers an operator needs: the
// address first, then whether the key reaches this branch at all, then whether
// the branch has to be created. A key scoped to staging that aims at
// production hears "not allowed on production", not "cannot create branches".
func (s *ApiKeyAccessService) Authorize(ctx context.Context, req CliRequest) error {
	if s.repo == nil || !s.licenseValid() {
		return nil
	}
	access, err := s.repo.GetAccess(ctx, req.APIKeyID)
	if err != nil {
		return unjudged(err)
	}
	if len(access.AllowedIps) > 0 && !ipAllowed(req.ClientIP, access.AllowedIps) {
		return ipNotAllowedError(req.ClientIP)
	}
	if !AllowsBranch(access.BranchRules, req.Branch, req.Action) {
		return deniedError(req.Action, req.Branch)
	}
	// Only publishing creates a branch (deployment_service.go upserts it on
	// the upload-url request and again when the update is sealed), and only a
	// key denied creation pays for the lookup.
	if req.Action == ActionPublish && !access.AllowBranchCreation {
		exists, err := s.repo.BranchExists(ctx, req.AppID, req.Branch)
		if err != nil {
			return unjudged(err)
		}
		if !exists {
			return branchCreationDeniedError(req.Branch)
		}
	}
	return nil
}

// recordAccessEvent reports one access mutation, actor = the dashboard
// principal on the request context (both routes are permission-gated
// dashboard mutations).
func (s *ApiKeyAccessService) recordAccessEvent(ctx context.Context, action auditlog.Action, targetType string, targetID string, targetDisplay string, appID string, metadata map[string]any) {
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
