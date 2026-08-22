// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package sso

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"xprem/config"
	"xprem/ee/licensing"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/services"
	"xprem/internal/store"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"
)

// SSOConfig is the deployment's OIDC client configuration, one provider per
// server, stored in the sso_config singleton row. ClientSecret is the
// unsealed plaintext: it never leaves the service layer.
type SSOConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	ProviderName string
	Scopes       string
	Enabled      bool
	// Optional sign-in restrictions, both empty by default (anyone the IdP
	// authenticates may sign in). When set, the email's domain must be one of
	// AllowedEmailDomains, and the id_token claim named GroupsClaim must carry
	// at least one of AllowedGroups.
	AllowedEmailDomains []string
	AllowedGroups       []string
	GroupsClaim         string
	// TrustUnverifiedEmail lifts the email_verified requirement. False by
	// default: an email is only used for domain authorization and account
	// lookup/linking when the id_token asserts email_verified=true, so an
	// attacker who can set an arbitrary unverified email at the IdP cannot
	// take over an existing account by matching its address. Admins turn it on
	// for a trusted provider that omits email_verified (notably Entra ID) on a
	// single tenant where users cannot self-assert addresses.
	TrustUnverifiedEmail bool
	// ManualUserValidation provisions newly discovered accounts disabled, so an
	// admin approves each one on the Users page before it can sign in. It is
	// the answer to "everyone in my Google Workspace can reach the dashboard"
	// for providers that do not emit group claims: the allowlist lives here
	// rather than at the IdP. Accounts linked to a pre-existing user are
	// untouched, and turning the toggle on never disables existing accounts.
	ManualUserValidation bool
}

// SSORepository persists the singleton configuration and the mapping from
// OIDC identities (issuer, subject) to dashboard users.
type SSORepository interface {
	// GetConfig returns nil (no error) when SSO has never been configured. An
	// unreadable client secret (master key changed) is reported by wrapping
	// ErrClientSecretUnreadable while still returning the row's other fields.
	GetConfig(ctx context.Context) (*SSOConfig, error)
	SaveConfig(ctx context.Context, cfg SSOConfig) error
	DeleteConfig(ctx context.Context) error
	FindUserBySubject(ctx context.Context, issuer string, subject string) (store.User, error)
	LinkIdentity(ctx context.Context, issuer string, subject string, userID string, email string) error
	// ProvisionUser creates the user row and its identity atomically, so a
	// crash between the two cannot leave an orphan account.
	ProvisionUser(ctx context.Context, params store.InsertUserParameters, issuer string, subject string) (store.User, error)
	TouchLastLogin(ctx context.Context, issuer string, subject string) error
}

var (
	ErrSSORequiresControlPlane = errors.New("single sign-on is managed in the database: this deployment runs in stateless mode, which is community edition only")
	ErrSSORequiresValidLicense = errors.New("single sign-on requires an active enterprise license")
	ErrSSONotConfigured        = errors.New("single sign-on is not configured")
	ErrSSOEmailMissing         = errors.New("the identity provider did not return a usable email address")
	// ErrSSOEmailUnverified is answered when the id_token carries an email the
	// provider has not verified (email_verified is false or absent) and the
	// admin has not opted into trusting unverified emails. Trusting it would
	// let an attacker who set an arbitrary email at the IdP link to, or
	// provision under, someone else's address.
	ErrSSOEmailUnverified = errors.New("the identity provider did not verify this email address")
	// ErrSSOAccessRestricted is answered when the token verified but the
	// account falls outside the configured domain/group restrictions.
	ErrSSOAccessRestricted = errors.New("this account is not allowed to sign in to this dashboard")
	// ErrSSOAccountPendingApproval is answered when the identity verified and
	// the account exists, but an admin has not approved it (manual user
	// validation) or has revoked it. Distinct from ErrSSOAccessRestricted so
	// the login page can tell the person to wait rather than to go away.
	ErrSSOAccountPendingApproval = errors.New("this account is waiting for an administrator to approve it")
	// ErrClientSecretUnreadable marks a stored client secret that can no
	// longer be unsealed (the DB keys master key changed). Sign-ins fail until
	// an admin re-enters the secret, which re-seals it under the current key.
	ErrClientSecretUnreadable = errors.New("the stored client secret cannot be decrypted with the current DB keys master key: re-enter the client secret to re-seal it")
)

// ConfigValidationError wraps everything that makes a submitted configuration
// unusable (bad input, or an issuer whose OIDC discovery fails) so the
// handler answers 400 with the actionable reason instead of an opaque 500.
type ConfigValidationError struct {
	Reason error
}

func (e *ConfigValidationError) Error() string { return e.Reason.Error() }
func (e *ConfigValidationError) Unwrap() error { return e.Reason }

// Flow-token claims. The subject and type are deliberately distinct from the
// dashboard session JWTs ("admin-dashboard" / "token") signed with the same
// JWT_SECRET, so neither token kind can ever be accepted in place of the other.
const (
	flowSubject   = "sso-flow"
	flowClaimType = "ssoState"
	// flowTTL bounds one round-trip to the IdP's sign-in page. Past it the
	// flow cookie's JWT expires and the user just restarts the flow.
	flowTTL = 10 * time.Minute
)

// LoginRedirect is everything the HTTP layer needs to send a browser to the
// IdP: the authorization URL, and the signed flow token to drop in a cookie
// so any replica can complete the callback without shared storage.
type LoginRedirect struct {
	AuthURL   string
	FlowToken string
}

// PublicConfig is the pre-auth view served to the login page: just enough to
// render the SSO button, nothing about the provider's internals.
type PublicConfig struct {
	Enabled      bool   `json:"enabled"`
	ProviderName string `json:"providerName,omitempty"`
}

// AdminConfig is the dashboard-facing view of the stored configuration. The
// client secret never leaves the server: only the fact that one is stored.
type AdminConfig struct {
	Issuer               string
	ClientID             string
	HasClientSecret      bool
	ProviderName         string
	Scopes               string
	Enabled              bool
	AllowedEmailDomains  []string
	AllowedGroups        []string
	GroupsClaim          string
	TrustUnverifiedEmail bool
	ManualUserValidation bool
	RedirectURI          string
}

// SaveConfigInput is one admin submission from the dashboard. An empty
// ClientSecret on an update means "keep the stored one" (the dashboard never
// sees the secret back, so it cannot resubmit it).
type SaveConfigInput struct {
	Issuer               string
	ClientID             string
	ClientSecret         string
	ProviderName         string
	Scopes               string
	Enabled              bool
	AllowedEmailDomains  []string
	AllowedGroups        []string
	GroupsClaim          string
	TrustUnverifiedEmail bool
	ManualUserValidation bool
}

// SSOService owns the OIDC sign-in flow (authorization code + PKCE against a
// confidential client), the JIT provisioning of member accounts, and the
// management of the stored configuration. The configuration is re-read from
// the database on every operation: SSO traffic is low, and it makes a change
// saved on one replica effective everywhere immediately.
type SSOService struct {
	repo     SSORepository
	userRepo services.UserRepository
	sessions *services.DashboardAuthService
	// onAuditEvent is the audit emission seam; nil means SSO sign-ins leave
	// no events.
	onAuditEvent auditlog.RecordFunc
	// licenseValid is the live licensing state; a field so same-package tests
	// can pin it without minting signed keys.
	licenseValid func() bool
	secret       string
	// httpClient talks to the IdP (discovery, JWKS, token endpoint). One
	// client with a hard timeout so a hung IdP can never pin a request.
	httpClient *http.Client

	// Discovery cache, keyed by issuer: filled lazily on the first sign-in
	// (never at boot), replaced whenever the configured issuer changes.
	// mu guards the cache and cooldown fields below, never a network call:
	// discoveries run outside of it, coalesced per issuer by discoveryGroup.
	mu             sync.Mutex
	cachedIssuer   string
	cachedProvider *oidc.Provider
	discoveryGroup singleflight.Group
	// Failure cooldown of the sign-in paths (see discoveryFailureCooldown).
	failedIssuer string
	failedAt     time.Time
	failedErr    error
}

// NewSSOService accepts a nil repository (stateless mode); every flow then
// answers ErrSSORequiresControlPlane and Enabled stays false.
func NewSSOService(repo SSORepository, userRepo services.UserRepository, sessions *services.DashboardAuthService) *SSOService {
	return &SSOService{
		repo:         repo,
		userRepo:     userRepo,
		sessions:     sessions,
		licenseValid: licensing.IsEnterprise,
		secret:       config.GetEnv("JWT_SECRET"),
		httpClient:   &http.Client{Timeout: 15 * time.Second},
	}
}

// activeConfig is the single gate for sign-in flows: control plane present,
// configuration stored and enabled, license valid. The license is checked on
// the server for every flow; hiding the dashboard button is only UX.
func (s *SSOService) activeConfig(ctx context.Context) (*SSOConfig, error) {
	if s.repo == nil {
		return nil, ErrSSORequiresControlPlane
	}
	cfg, err := s.repo.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg == nil || !cfg.Enabled {
		return nil, ErrSSONotConfigured
	}
	if !s.licenseValid() {
		return nil, ErrSSORequiresValidLicense
	}
	return cfg, nil
}

// Enabled reports whether SSO is currently active. It doubles as the
// enforcement signal injected into the community services (password login of
// members, manual user creation): any failure reads as inactive, so a
// database hiccup or an expired license can never lock password sign-in out.
func (s *SSOService) Enabled(ctx context.Context) bool {
	_, err := s.activeConfig(ctx)
	return err == nil
}

// PublicConfig is served pre-auth to the login page. Errors deliberately
// collapse to "disabled": the login page has no use for the reason.
func (s *SSOService) PublicConfig(ctx context.Context) PublicConfig {
	cfg, err := s.activeConfig(ctx)
	if err != nil {
		return PublicConfig{}
	}
	return PublicConfig{Enabled: true, ProviderName: cfg.ProviderName}
}

// RedirectURI is derived from BASE_URL, never configured: it is displayed
// read-only in the dashboard for copy-pasting into the IdP's app settings.
func (s *SSOService) RedirectURI() string {
	return strings.TrimRight(config.GetEnv("BASE_URL"), "/") + "/auth/sso/callback"
}

func (s *SSOService) adminView(cfg *SSOConfig) *AdminConfig {
	return &AdminConfig{
		Issuer:               cfg.Issuer,
		ClientID:             cfg.ClientID,
		HasClientSecret:      cfg.ClientSecret != "",
		ProviderName:         cfg.ProviderName,
		Scopes:               cfg.Scopes,
		Enabled:              cfg.Enabled,
		AllowedEmailDomains:  cfg.AllowedEmailDomains,
		AllowedGroups:        cfg.AllowedGroups,
		GroupsClaim:          cfg.GroupsClaim,
		TrustUnverifiedEmail: cfg.TrustUnverifiedEmail,
		ManualUserValidation: cfg.ManualUserValidation,
		RedirectURI:          s.RedirectURI(),
	}
}

// GetAdminConfig returns the stored configuration for the dashboard card.
// Not license-gated, like the apikeyrestrictions reads: the dashboard can
// always show what is stored. An unreadable client secret is reported as
// HasClientSecret false so the card prompts the admin to re-enter it.
func (s *SSOService) GetAdminConfig(ctx context.Context) (*AdminConfig, error) {
	if s.repo == nil {
		return nil, ErrSSORequiresControlPlane
	}
	cfg, err := s.repo.GetConfig(ctx)
	if err != nil && !errors.Is(err, ErrClientSecretUnreadable) {
		return nil, err
	}
	if cfg == nil {
		return nil, ErrSSONotConfigured
	}
	return s.adminView(cfg), nil
}

// SaveConfig validates and persists an admin submission. Validation includes
// a live OIDC discovery against the issuer: the IdP's error is surfaced
// verbatim to the admin and nothing is persisted, which is the debugging
// experience the dashboard-managed configuration exists for.
func (s *SSOService) SaveConfig(ctx context.Context, input SaveConfigInput) (*AdminConfig, error) {
	if s.repo == nil {
		return nil, ErrSSORequiresControlPlane
	}
	if !s.licenseValid() {
		return nil, ErrSSORequiresValidLicense
	}
	cfg, err := normalizeConfigInput(input)
	if err != nil {
		return nil, err
	}
	if cfg.ClientSecret == "" {
		// The dashboard sends an empty secret to mean "keep the stored one".
		existing, err := s.repo.GetConfig(ctx)
		if err != nil {
			// Includes ErrClientSecretUnreadable: there is nothing usable to
			// keep, the admin must submit the secret again.
			return nil, err
		}
		if existing == nil {
			return nil, &ConfigValidationError{Reason: errors.New("a client secret is required")}
		}
		// The stored secret belongs to the issuer it was entered for. Carrying
		// it to another one would send it to whatever token endpoint that
		// issuer advertises, and the dashboard never reads the secret back, so
		// the admin making the change need not know it. Rotating the client id
		// against the same issuer stays allowed: the secret goes back to the
		// IdP that already holds it.
		if existing.Issuer != cfg.Issuer {
			return nil, &ConfigValidationError{
				Reason: errors.New("re-enter the client secret when changing the issuer"),
			}
		}
		cfg.ClientSecret = existing.ClientSecret
	}
	// Discovery only guards configurations that are meant to be used: saving
	// a disabled one must always succeed, so an admin can turn SSO off from
	// the dashboard while the IdP itself is down (the break-glass path).
	// Re-enabling runs the live check again.
	if cfg.Enabled {
		if _, err := s.discoverProvider(cfg.Issuer); err != nil {
			return nil, &ConfigValidationError{Reason: fmt.Errorf("OIDC discovery failed for %s: %w", cfg.Issuer, err)}
		}
	}
	if err := s.repo.SaveConfig(ctx, *cfg); err != nil {
		return nil, err
	}
	// The security toggles are the point of this entry: "who weakened this
	// control and when" must be answerable from the log alone.
	s.recordConfigEvent(ctx, auditlog.ActionSSOConfigSaved, map[string]any{
		"issuer":                 cfg.Issuer,
		"enabled":                cfg.Enabled,
		"manual_user_validation": cfg.ManualUserValidation,
		"trust_unverified_email": cfg.TrustUnverifiedEmail,
		"allowed_email_domains":  cfg.AllowedEmailDomains,
		"allowed_groups":         cfg.AllowedGroups,
	})
	return s.adminView(cfg), nil
}

func (s *SSOService) DeleteConfig(ctx context.Context) error {
	if s.repo == nil {
		return ErrSSORequiresControlPlane
	}
	if !s.licenseValid() {
		return ErrSSORequiresValidLicense
	}
	if err := s.repo.DeleteConfig(ctx); err != nil {
		return err
	}
	s.recordConfigEvent(ctx, auditlog.ActionSSOConfigDeleted, nil)
	return nil
}

// BeginLogin mints the per-login state (state, nonce, PKCE verifier), seals
// it into a short-lived signed flow token and builds the IdP authorization
// URL. Nothing is stored server-side: the flow token travels in a cookie, so
// any replica can complete the callback.
func (s *SSOService) BeginLogin(ctx context.Context) (*LoginRedirect, error) {
	cfg, err := s.activeConfig(ctx)
	if err != nil {
		return nil, err
	}
	provider, err := s.provider(cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery failed for %s: %w", cfg.Issuer, err)
	}
	state, err := randomToken()
	if err != nil {
		return nil, err
	}
	nonce, err := randomToken()
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()
	flowToken, err := crypto.GenerateJWTToken(s.secret, flowClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   flowSubject,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(flowTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		Type:     flowClaimType,
		State:    state,
		Nonce:    nonce,
		Verifier: verifier,
	})
	if err != nil {
		return nil, fmt.Errorf("error while generating the sso flow token: %w", err)
	}
	authURL := s.oauthConfig(cfg, provider).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	return &LoginRedirect{AuthURL: authURL, FlowToken: flowToken}, nil
}

// CompleteLogin is the callback half of the flow: it checks the flow token
// and state, exchanges the code (PKCE), verifies the id_token and its nonce,
// applies the sign-in restrictions, resolves or provisions the account and
// mints the ordinary dashboard session pair.
func (s *SSOService) CompleteLogin(ctx context.Context, flowToken string, state string, code string) (*services.DashboardSession, error) {
	cfg, err := s.activeConfig(ctx)
	if err != nil {
		return nil, err
	}
	flow, err := s.verifyFlowToken(flowToken)
	if err != nil {
		return nil, fmt.Errorf("invalid sso flow token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(state), []byte(flow.state)) != 1 {
		return nil, errors.New("state mismatch in the sso callback")
	}
	provider, err := s.provider(cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery failed for %s: %w", cfg.Issuer, err)
	}
	clientCtx := oidc.ClientContext(ctx, s.httpClient)
	token, err := s.oauthConfig(cfg, provider).Exchange(clientCtx, code, oauth2.VerifierOption(flow.verifier))
	if err != nil {
		return nil, fmt.Errorf("authorization code exchange failed: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, errors.New("the token response carries no id_token")
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(clientCtx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("id_token verification failed: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(flow.nonce)) != 1 {
		return nil, errors.New("nonce mismatch in the id_token")
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("could not parse the id_token claims: %w", err)
	}
	email, emailVerified, err := emailFromClaims(claims)
	if err != nil {
		return nil, err
	}
	// The email drives domain authorization and account lookup/linking below,
	// so it must be one the provider vouches for, not one the user typed.
	// Gate it before any of those uses.
	if !emailVerified && !cfg.TrustUnverifiedEmail {
		err := fmt.Errorf("%w: %q is not verified (enable trust for this provider if it does not emit email_verified)", ErrSSOEmailUnverified, email)
		s.recordSSOLoginFailure(ctx, email, err)
		return nil, err
	}
	if err := checkSignInRestrictions(cfg, email, claims); err != nil {
		s.recordSSOLoginFailure(ctx, email, err)
		return nil, err
	}
	user, err := s.resolveUser(ctx, cfg, idToken.Subject, email)
	if err != nil {
		return nil, err
	}
	// Answered before IssueSession, which refuses disabled accounts too: this
	// path just needs its own error so the login page can explain that the
	// account exists and is waiting, rather than showing a generic refusal.
	if !user.Enabled {
		err := fmt.Errorf("%w: %q is not approved yet", ErrSSOAccountPendingApproval, email)
		s.recordSSOLoginFailure(ctx, email, err)
		return nil, err
	}
	session, err := s.sessions.IssueSession(ctx, user)
	if err != nil {
		return nil, err
	}
	if s.onAuditEvent != nil {
		s.onAuditEvent(ctx, auditlog.Event{
			ActorType:     auditlog.ActorUser,
			ActorID:       user.Id,
			ActorDisplay:  user.Email,
			Action:        auditlog.ActionUserSSOLogin,
			TargetType:    "user",
			TargetID:      user.Id,
			TargetDisplay: user.Email,
			Outcome:       auditlog.OutcomeSuccess,
		})
	}
	return session, nil
}

// recordSSOLoginFailure records the deliberate sign-in refusals, the probing
// signal a security review asks for: unverified email, domain/group
// restrictions, and accounts awaiting approval (the SSO mirror of
// DashboardAuthService.recordLoginFailure). Protocol and infrastructure
// failures (state mismatch, provider unreachable) are not sign-in decisions
// and stay out of the log, hence the sentinel allowlist.
func (s *SSOService) recordSSOLoginFailure(ctx context.Context, email string, err error) {
	if s.onAuditEvent == nil {
		return
	}
	var reason string
	switch {
	case errors.Is(err, ErrSSOEmailUnverified):
		reason = "email_unverified"
	case errors.Is(err, ErrSSOAccessRestricted):
		reason = "access_restricted"
	case errors.Is(err, ErrSSOAccountPendingApproval):
		reason = "pending_approval"
	default:
		return
	}
	// The account may not exist yet (JIT provisioning): the attempted email
	// is the only identity a failure can carry, so the actor id stays empty.
	s.onAuditEvent(ctx, auditlog.Event{
		ActorType:     auditlog.ActorUser,
		ActorDisplay:  email,
		Action:        auditlog.ActionUserSSOLogin,
		TargetType:    "user",
		TargetDisplay: email,
		Outcome:       auditlog.OutcomeFailure,
		Metadata:      map[string]any{"reason": reason},
	})
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe: without it, SSO
// sign-ins simply leave no audit events.
func (s *SSOService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// recordConfigEvent reports an admin SSO-configuration action, actor = the
// admin principal on the request context (both routes are admin-gated). The
// metadata never carries the client secret.
func (s *SSOService) recordConfigEvent(ctx context.Context, action auditlog.Action, metadata map[string]any) {
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
		TargetType:   "sso_config",
		TargetID:     "sso_config",
		Outcome:      auditlog.OutcomeSuccess,
		Metadata:     metadata,
	})
}

type flowState struct {
	state    string
	nonce    string
	verifier string
}

// flowClaims is the claim set of the token that carries an SSO login across
// the redirect to the IdP and back.
type flowClaims struct {
	jwt.RegisteredClaims
	Type     string `json:"type"`
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
}

func (s *SSOService) verifyFlowToken(flowToken string) (*flowState, error) {
	claims := flowClaims{}
	if _, err := crypto.DecodeAndExtractJWTToken(s.secret, flowToken, &claims); err != nil {
		return nil, err
	}
	if claims.Type != flowClaimType || claims.Subject != flowSubject {
		return nil, errors.New("not an sso flow token")
	}
	flow := &flowState{state: claims.State, nonce: claims.Nonce, verifier: claims.Verifier}
	if flow.state == "" || flow.nonce == "" || flow.verifier == "" {
		return nil, errors.New("incomplete sso flow token")
	}
	return flow, nil
}

func (s *SSOService) oauthConfig(cfg *SSOConfig, provider *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  s.RedirectURI(),
		Endpoint:     provider.Endpoint(),
		Scopes:       strings.Fields(cfg.Scopes),
	}
}

// discoveryFailureCooldown is how long the sign-in paths fail fast after a
// discovery failure instead of re-hitting an unreachable IdP: each attempt
// can cost up to the 15s HTTP timeout, and stacking those per sign-in click
// would pile up. Bounded and small, so recovery is picked up quickly.
const discoveryFailureCooldown = 10 * time.Second

// provider returns the discovery document for the issuer, cached across
// sign-ins and rebuilt whenever the configured issuer changes. The mutex only
// guards the cache fields; the network fetch itself runs outside of it,
// coalesced by the single-flight group.
func (s *SSOService) provider(issuer string) (*oidc.Provider, error) {
	s.mu.Lock()
	if s.cachedProvider != nil && s.cachedIssuer == issuer {
		provider := s.cachedProvider
		s.mu.Unlock()
		return provider, nil
	}
	if s.failedIssuer == issuer && s.failedErr != nil && time.Since(s.failedAt) < discoveryFailureCooldown {
		err := s.failedErr
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	return s.discoverProvider(issuer)
}

// discoverProvider always performs a live discovery (SaveConfig's "save and
// test" must actually test, so it never short-circuits on the failure
// cooldown) and replaces the cache with the result. Concurrent callers for
// the same issuer share one in-flight request.
func (s *SSOService) discoverProvider(issuer string) (*oidc.Provider, error) {
	result, err, _ := s.discoveryGroup.Do(issuer, func() (interface{}, error) {
		return s.discover(issuer)
	})
	if err != nil {
		return nil, err
	}
	return result.(*oidc.Provider), nil
}

// discover performs the network fetch with no lock held, then records the
// outcome: the cache on success, the failure cooldown on error.
func (s *SSOService) discover(issuer string) (*oidc.Provider, error) {
	// The context deliberately outlives the calling request: go-oidc keeps it
	// for the JWKS refreshes of future id_token verifications. The HTTP
	// timeout on httpClient is what bounds each fetch.
	provider, err := oidc.NewProvider(oidc.ClientContext(context.Background(), s.httpClient), issuer)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.failedIssuer = issuer
		s.failedAt = time.Now()
		s.failedErr = err
		return nil, err
	}
	s.cachedIssuer = issuer
	s.cachedProvider = provider
	s.failedIssuer = ""
	s.failedErr = nil
	return provider, nil
}

// resolveUser maps a verified identity onto a dashboard account:
// known subject first (stable even when the email changes at the IdP), then
// account linking by email, then JIT provisioning of a non-admin member.
func (s *SSOService) resolveUser(ctx context.Context, cfg *SSOConfig, subject string, email string) (store.User, error) {
	user, err := s.lookupOrProvision(ctx, cfg, subject, email)
	if err != nil {
		// A concurrent first sign-in handled by another replica may have
		// provisioned or linked the same identity between our lookup and our
		// write; one retry then finds it by subject.
		if alreadyExistsErr := (*store.ErrResourceAlreadyExists)(nil); errors.As(err, &alreadyExistsErr) {
			return s.lookupOrProvision(ctx, cfg, subject, email)
		}
		return store.User{}, err
	}
	return user, nil
}

func (s *SSOService) lookupOrProvision(ctx context.Context, cfg *SSOConfig, subject string, email string) (store.User, error) {
	issuer := cfg.Issuer
	user, err := s.repo.FindUserBySubject(ctx, issuer, subject)
	if err == nil {
		// Best effort: a failed touch must not fail the sign-in.
		if touchErr := s.repo.TouchLastLogin(ctx, issuer, subject); touchErr != nil {
			log.Printf("[SSO] failed to record the sign-in of subject %q: %v", subject, touchErr)
		}
		return user, nil
	}
	if notFoundErr := (*store.ErrResourceNotFound)(nil); !errors.As(err, &notFoundErr) {
		return store.User{}, err
	}
	existing, err := s.userRepo.GetUserByEmail(ctx, email)
	if err == nil {
		// First SSO sign-in of an account that already exists with this email:
		// link it instead of duplicating. Role, password and enabled flag are
		// untouched, so turning manual validation on never demands re-approval
		// of accounts that already had access.
		if err := s.repo.LinkIdentity(ctx, issuer, subject, existing.Id, email); err != nil {
			return store.User{}, err
		}
		// A new sign-in door just opened on an existing account (possibly an
		// admin's): the binding is its own security event, distinct from the
		// sso_login that follows it.
		if s.onAuditEvent != nil {
			s.onAuditEvent(ctx, auditlog.Event{
				ActorType:     auditlog.ActorSystem,
				ActorID:       "sso",
				ActorDisplay:  "SSO provisioning",
				Action:        auditlog.ActionUserSSOLinked,
				TargetType:    "user",
				TargetID:      existing.Id,
				TargetDisplay: existing.Email,
				Outcome:       auditlog.OutcomeSuccess,
				Metadata:      map[string]any{"issuer": issuer, "subject": subject},
			})
		}
		return existing, nil
	}
	if notFoundErr := (*store.ErrResourceNotFound)(nil); !errors.As(err, &notFoundErr) {
		return store.User{}, err
	}
	// JIT provisioning: always a non-admin member; promotion happens on the
	// Users page. The empty password hash can never verify against bcrypt, so
	// the account is SSO-only until SSO is turned off and an admin intervenes.
	// Under manual validation the row lands disabled and the sign-in that
	// created it is refused, so the admin has something concrete to approve.
	user, err = s.repo.ProvisionUser(ctx, store.InsertUserParameters{
		ID:           uuid.New().String(),
		Email:        email,
		PasswordHash: "",
		IsAdmin:      false,
		Enabled:      !cfg.ManualUserValidation,
	}, issuer, subject)
	if err != nil {
		return store.User{}, err
	}
	// The IdP created this account, no dashboard principal exists yet: the
	// actor is the system, the identity facts go to the metadata.
	if s.onAuditEvent != nil {
		s.onAuditEvent(ctx, auditlog.Event{
			ActorType:     auditlog.ActorSystem,
			ActorID:       "sso",
			ActorDisplay:  "SSO provisioning",
			Action:        auditlog.ActionUserSSOProvisioned,
			TargetType:    "user",
			TargetID:      user.Id,
			TargetDisplay: user.Email,
			Outcome:       auditlog.OutcomeSuccess,
			Metadata: map[string]any{
				"issuer":           issuer,
				"subject":          subject,
				"pending_approval": cfg.ManualUserValidation,
			},
		})
	}
	return user, nil
}

// emailFromClaims resolves the account email. Entra commonly omits the email
// claim (it is optional in its token configuration) while putting the address
// in preferred_username, hence the fallback; anything that does not parse as
// a plain address is ignored.
//
// It also reports whether the address is verified. Only the standard email
// claim can be, via email_verified; an address recovered from
// preferred_username is never treated as verified, since email_verified says
// nothing about that claim.
func emailFromClaims(claims map[string]any) (email string, verified bool, err error) {
	if candidate, ok := parseEmailClaim(claims["email"]); ok {
		return candidate, claimIsTrue(claims["email_verified"]), nil
	}
	if candidate, ok := parseEmailClaim(claims["preferred_username"]); ok {
		return candidate, false, nil
	}
	return "", false, ErrSSOEmailMissing
}

// parseEmailClaim normalizes a claim value and accepts it only if it is a
// bare email address (no display-name form).
func parseEmailClaim(value any) (string, bool) {
	raw, _ := value.(string)
	candidate := store.NormalizeEmail(raw)
	if candidate == "" {
		return "", false
	}
	if addr, err := mail.ParseAddress(candidate); err == nil && addr.Address == candidate {
		return candidate, true
	}
	return "", false
}

// claimIsTrue reads a boolean claim that IdPs encode either as a JSON boolean
// or as the string "true".
func claimIsTrue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(typed, "true")
	}
	return false
}

// checkSignInRestrictions applies the optional domain and group allowlists.
// Both must pass when both are configured. The wrapped detail is logged
// server-side by the handler; the browser only ever sees a generic code.
func checkSignInRestrictions(cfg *SSOConfig, email string, claims map[string]any) error {
	if len(cfg.AllowedEmailDomains) > 0 {
		domain := email[strings.LastIndex(email, "@")+1:]
		if !slices.Contains(cfg.AllowedEmailDomains, domain) {
			return fmt.Errorf("%w: the email domain %q is not in the allowed domains", ErrSSOAccessRestricted, domain)
		}
	}
	if len(cfg.AllowedGroups) > 0 {
		groups := stringsFromClaim(claims[cfg.GroupsClaim])
		allowed := slices.ContainsFunc(groups, func(group string) bool {
			return slices.Contains(cfg.AllowedGroups, group)
		})
		if !allowed {
			return fmt.Errorf("%w: the %q claim carries none of the allowed groups (the IdP sent %d group(s))", ErrSSOAccessRestricted, cfg.GroupsClaim, len(groups))
		}
	}
	return nil
}

// stringsFromClaim accepts the two shapes IdPs use for multi-value claims: a
// JSON array of strings, or a single plain string.
func stringsFromClaim(value any) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			if str, ok := entry.(string); ok && str != "" {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// normalizeConfigInput turns one dashboard submission into a storable
// configuration, or a ConfigValidationError naming the first problem.
func normalizeConfigInput(input SaveConfigInput) (*SSOConfig, error) {
	invalid := func(format string, args ...any) (*SSOConfig, error) {
		return nil, &ConfigValidationError{Reason: fmt.Errorf(format, args...)}
	}
	issuer := strings.TrimSpace(input.Issuer)
	if issuer == "" {
		return invalid("the issuer URL is required")
	}
	issuerURL, err := url.Parse(issuer)
	if err != nil || issuerURL.Host == "" {
		return invalid("the issuer must be a valid URL, e.g. https://login.microsoftonline.com/{tenant-id}/v2.0")
	}
	if issuerURL.Scheme != "https" && !(issuerURL.Scheme == "http" && isLoopbackHost(issuerURL.Hostname())) {
		return invalid("the issuer must use https (plain http is only accepted for a loopback address, for local testing)")
	}
	clientID := strings.TrimSpace(input.ClientID)
	if clientID == "" {
		return invalid("the client id is required")
	}
	providerName := strings.TrimSpace(input.ProviderName)
	if providerName == "" {
		providerName = "SSO"
	}
	if len(providerName) > 60 {
		return invalid("the provider name must stay under 60 characters")
	}
	scopes := strings.Join(strings.Fields(input.Scopes), " ")
	if scopes == "" {
		scopes = "openid profile email"
	}
	if !slices.Contains(strings.Fields(scopes), "openid") {
		return invalid("the scopes must include \"openid\"")
	}
	groupsClaim := strings.TrimSpace(input.GroupsClaim)
	if groupsClaim == "" {
		groupsClaim = "groups"
	}
	domains, err := normalizeDomains(input.AllowedEmailDomains)
	if err != nil {
		return nil, err
	}
	return &SSOConfig{
		Issuer:               issuer,
		ClientID:             clientID,
		ClientSecret:         strings.TrimSpace(input.ClientSecret),
		ProviderName:         providerName,
		Scopes:               scopes,
		Enabled:              input.Enabled,
		AllowedEmailDomains:  domains,
		AllowedGroups:        normalizeList(input.AllowedGroups),
		GroupsClaim:          groupsClaim,
		TrustUnverifiedEmail: input.TrustUnverifiedEmail,
		ManualUserValidation: input.ManualUserValidation,
	}, nil
}

// normalizeDomains lowercases, strips a leading "@" (people paste
// "@acme.com") and deduplicates; anything that still looks wrong is refused
// so a typo cannot silently lock everyone out.
func normalizeDomains(domains []string) ([]string, error) {
	out := make([]string, 0, len(domains))
	for _, domain := range domains {
		normalized := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "@"))
		if normalized == "" {
			continue
		}
		if strings.ContainsAny(normalized, "@ /") {
			return nil, &ConfigValidationError{Reason: fmt.Errorf("%q is not a valid email domain", domain)}
		}
		if !slices.Contains(out, normalized) {
			out = append(out, normalized)
		}
	}
	return out, nil
}

func normalizeList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if !slices.Contains(out, trimmed) {
			out = append(out, trimmed)
		}
	}
	return out
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("failed to source system entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
