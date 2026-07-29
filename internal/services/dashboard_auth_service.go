package services

import (
	"context"
	"errors"
	"expo-open-ota/config"
	"expo-open-ota/internal/auditlog"
	"expo-open-ota/internal/crypto"
	"expo-open-ota/internal/store"
	"fmt"
	"log"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// DashboardAuthService owns the admin dashboard's own session credentials: a
// short-lived session JWT and a long-lived refresh JWT, minted after login.
type DashboardAuthService struct {
	Secret   string
	userRepo UserRepository
	// refreshTokens is nil in stateless mode, where the refresh token stays
	// the unrotated 7-day credential it has always been.
	refreshTokens RefreshTokenRepository
	// onAuditEvent is nil in community edition, where sign-ins are not recorded.
	onAuditEvent auditlog.RecordFunc
	// ssoEnforced is nil unless the enterprise wiring injects it, meaning SSO
	// is never enforced.
	ssoEnforced func(context.Context) bool
}

// DashboardSession is the JWT pair handed to the dashboard on login or refresh.
type DashboardSession struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
}

// DashboardPrincipal identifies who is behind a validated dashboard session.
// UserId is empty in stateless mode, where the ADMIN_EMAIL account is not a
// database row.
type DashboardPrincipal struct {
	UserId  string
	Email   string
	IsAdmin bool
	// SessionVersion is always 0 in stateless mode, which has no users table
	// to bump.
	SessionVersion int32
}

// RefreshTokenRepository is the refresh-token rotation ledger. Like
// UserRepository it only exists on the control plane.
type RefreshTokenRepository interface {
	InsertRefreshToken(ctx context.Context, params store.InsertRefreshTokenParameters) error
	// RotateRefreshToken retires one token and issues its successor in the same
	// family, atomically.
	RotateRefreshToken(ctx context.Context, params store.RotateRefreshTokenParameters) (store.RefreshToken, error)
	GetRefreshToken(ctx context.Context, id string, replayGrace time.Duration) (store.RefreshToken, error)
	DeleteRefreshTokenFamily(ctx context.Context, familyId string) error
	DeleteExpiredRefreshTokens(ctx context.Context, userId string) error
}

// ErrAdminEmailNotSet is surfaced verbatim to the login response: an operator
// hitting it needs the instruction, not a generic 401.
var ErrAdminEmailNotSet = errors.New("ADMIN_EMAIL is not set: stateless mode logs into the dashboard with ADMIN_EMAIL and ADMIN_PASSWORD. Set ADMIN_EMAIL on the server and retry")

// ErrPasswordLoginDisabledBySSO is returned to member accounts that present a
// valid password while SSO is enforced.
var ErrPasswordLoginDisabledBySSO = errors.New("password sign-in is disabled while SSO is active: sign in with SSO instead")

// ErrAccountPendingApproval is returned for an account whose enabled flag is off.
var ErrAccountPendingApproval = errors.New("this account is waiting for an administrator to approve it")

// dashboardSubject scopes every dashboard JWT to the dashboard itself, which
// keeps the upload tokens minted by localBucket (signed with the same
// JWT_SECRET) from being accepted here.
const dashboardSubject = "admin-dashboard"

const (
	sessionTokenTTL = 2 * time.Hour
	refreshTokenTTL = 7 * 24 * time.Hour
)

// refreshReplayGrace covers requests racing to refresh the same expiring
// token: arrivals within this window after the first get the same new pair
// instead of being treated as a stolen-token replay.
const refreshReplayGrace = 30 * time.Second

// ErrSessionRevoked is returned when a syntactically valid token no longer
// matches the account behind it: deleted, disabled, or its security
// generation moved on.
var ErrSessionRevoked = errors.New("this session is no longer valid: sign in again")

// ErrRefreshTokenReuse marks a refresh token presented after it was already
// rotated; the whole family is revoked before this is returned.
var ErrRefreshTokenReuse = errors.New("this refresh token was already used: every session from that sign-in has been revoked")

// NewDashboardAuthService accepts nil repositories for stateless mode, where
// logins are checked against ADMIN_EMAIL/ADMIN_PASSWORD instead.
func NewDashboardAuthService(userRepo UserRepository, refreshTokens RefreshTokenRepository) *DashboardAuthService {
	return &DashboardAuthService{
		Secret:        config.GetEnv("JWT_SECRET"),
		userRepo:      userRepo,
		refreshTokens: refreshTokens,
	}
}

// SetSSOEnforced injects the live "SSO is active" signal.
func (a *DashboardAuthService) SetSSOEnforced(enforced func(context.Context) bool) {
	a.ssoEnforced = enforced
}

// generateSessionToken mints the access half of a dashboard session.
func (a *DashboardAuthService) generateSessionToken(principal DashboardPrincipal) (*string, error) {
	token, err := crypto.GenerateJWTToken(a.Secret, jwt.MapClaims{
		"sub":     dashboardSubject,
		"exp":     time.Now().Add(sessionTokenTTL).Unix(),
		"iat":     time.Now().Unix(),
		"type":    "token",
		"userId":  principal.UserId,
		"email":   principal.Email,
		"isAdmin": principal.IsAdmin,
		"sv":      principal.SessionVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("error while generating the jwt token: %w", err)
	}
	return &token, nil
}

// generateRefreshToken mints the refresh half. tokenId names the ledger row
// that makes it single-use, and is empty in stateless mode.
func (a *DashboardAuthService) generateRefreshToken(principal DashboardPrincipal, tokenId string, expiresAt time.Time) (*string, error) {
	refreshToken, err := crypto.GenerateJWTToken(a.Secret, jwt.MapClaims{
		"sub":    dashboardSubject,
		"exp":    expiresAt.Unix(),
		"iat":    time.Now().Unix(),
		"type":   "refreshToken",
		"userId": principal.UserId,
		"email":  principal.Email,
		"sv":     principal.SessionVersion,
		"jti":    tokenId,
	})
	if err != nil {
		return nil, fmt.Errorf("error while generating the jwt token: %w", err)
	}
	return &refreshToken, nil
}

// issueSessionPair mints the two JWTs for a refresh token whose ledger row has
// already been written (or, in stateless mode, for no row at all).
func (a *DashboardAuthService) issueSessionPair(principal DashboardPrincipal, tokenId string, expiresAt time.Time) (*DashboardSession, error) {
	token, err := a.generateSessionToken(principal)
	if err != nil {
		return nil, err
	}
	refreshToken, err := a.generateRefreshToken(principal, tokenId, expiresAt)
	if err != nil {
		return nil, err
	}
	return &DashboardSession{
		Token:        *token,
		RefreshToken: *refreshToken,
	}, nil
}

// startSession opens a new refresh chain: a sign-in, whether by password, by
// SSO, or the replacement handed back after a password change.
func (a *DashboardAuthService) startSession(ctx context.Context, principal DashboardPrincipal) (*DashboardSession, error) {
	expiresAt := time.Now().Add(refreshTokenTTL)
	if a.refreshTokens == nil {
		return a.issueSessionPair(principal, "", expiresAt)
	}
	tokenId := uuid.New().String()
	if err := a.refreshTokens.InsertRefreshToken(ctx, store.InsertRefreshTokenParameters{
		ID:        tokenId,
		UserID:    principal.UserId,
		FamilyID:  uuid.New().String(),
		ExpiresAt: expiresAt,
	}); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
	}
	if err := a.refreshTokens.DeleteExpiredRefreshTokens(ctx, principal.UserId); err != nil {
		log.Printf("failed to purge expired refresh tokens for user %s: %v", principal.UserId, err)
	}
	return a.issueSessionPair(principal, tokenId, expiresAt)
}

// resolveStatelessPrincipal checks credentials against ADMIN_EMAIL and
// ADMIN_PASSWORD. When password is nil only the account's existence is
// resolved, for the refresh path.
func resolveStatelessPrincipal(email string, password *string) (*DashboardPrincipal, error) {
	adminEmail := store.NormalizeEmail(config.GetEnv("ADMIN_EMAIL"))
	if adminEmail == "" {
		return nil, ErrAdminEmailNotSet
	}
	adminPassword := config.GetEnv("ADMIN_PASSWORD")
	if adminPassword == "" {
		return nil, errors.New("admin password is not set, all requests will be rejected")
	}
	if store.NormalizeEmail(email) != adminEmail {
		return nil, errors.New("invalid credentials")
	}
	if password != nil && *password != adminPassword {
		return nil, errors.New("invalid credentials")
	}
	return &DashboardPrincipal{Email: adminEmail, IsAdmin: true}, nil
}

// unknownUserPasswordHash is a throwaway bcrypt hash checked when a login
// names an email with no account, so timing cannot enumerate accounts.
const unknownUserPasswordHash = "$2a$10$RTxsxJsH5d9yZcM.fDe/kOv28rciQYAnNBOrK0frmWJPZGH1pTzhO"

// ErrAuthUnavailable marks login/refresh failures caused by the account store
// being unreachable, which the handlers must surface as a 500.
var ErrAuthUnavailable = errors.New("could not verify the account against the database")

// principalForUser is the single choke point every database-backed sign-in
// path (password login, SSO callback, refresh) goes through, which is why the
// enabled check lives here.
func (a *DashboardAuthService) principalForUser(ctx context.Context, user store.User) (*DashboardPrincipal, error) {
	if !user.Enabled {
		return nil, ErrAccountPendingApproval
	}
	if err := a.userRepo.TouchUserLastConnected(ctx, user.Id); err != nil {
		log.Printf("failed to record last connection for user %s: %v", user.Id, err)
	}
	return &DashboardPrincipal{
		UserId:         user.Id,
		Email:          user.Email,
		IsAdmin:        user.IsAdmin,
		SessionVersion: user.SessionVersion,
	}, nil
}

func (a *DashboardAuthService) resolveLoginPrincipal(ctx context.Context, email string, password string) (*DashboardPrincipal, error) {
	if a.userRepo == nil {
		return resolveStatelessPrincipal(email, &password)
	}
	user, err := a.userRepo.GetUserByEmail(ctx, email)
	if err != nil {
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			// Burn the same bcrypt cost as a real comparison so timing cannot
			// enumerate which emails exist.
			crypto.VerifyPassword(unknownUserPasswordHash, password)
			return nil, errors.New("invalid credentials")
		}
		return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
	}
	if user.PasswordHash == "" {
		// SSO-provisioned accounts carry an empty hash; burn the same bcrypt
		// cost rather than let an empty hash short-circuit and leak via timing.
		crypto.VerifyPassword(unknownUserPasswordHash, password)
		return nil, errors.New("invalid credentials")
	}
	if !crypto.VerifyPassword(user.PasswordHash, password) {
		return nil, errors.New("invalid credentials")
	}
	if a.ssoEnforced != nil && a.ssoEnforced(ctx) && !user.IsAdmin {
		return nil, ErrPasswordLoginDisabledBySSO
	}
	return a.principalForUser(ctx, user)
}

// resolveRefreshPrincipal re-resolves the account behind a refresh token by
// its immutable user id, never by email.
func (a *DashboardAuthService) resolveRefreshPrincipal(ctx context.Context, userId string) (*DashboardPrincipal, error) {
	if userId == "" {
		return nil, errors.New("invalid token")
	}
	user, err := a.userRepo.GetUserByID(ctx, userId)
	if err != nil {
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			return nil, errors.New("invalid credentials")
		}
		return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
	}
	return a.principalForUser(ctx, user)
}

func (a *DashboardAuthService) LoginWithEmailPassword(ctx context.Context, email string, password string) (*DashboardSession, error) {
	principal, err := a.resolveLoginPrincipal(ctx, email, password)
	if err != nil {
		a.recordLoginFailure(ctx, email, err)
		return nil, err
	}
	a.recordLoginSuccess(ctx, principal)
	return a.startSession(ctx, *principal)
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe: without it,
// sign-ins simply leave no audit events.
func (a *DashboardAuthService) SetOnAuditEvent(record auditlog.RecordFunc) {
	a.onAuditEvent = record
}

func (a *DashboardAuthService) recordLoginSuccess(ctx context.Context, principal *DashboardPrincipal) {
	if a.onAuditEvent == nil {
		return
	}
	a.onAuditEvent(ctx, auditlog.Event{
		ActorType:     auditlog.ActorUser,
		ActorID:       principal.UserId,
		ActorDisplay:  principal.Email,
		Action:        auditlog.ActionUserLogin,
		TargetType:    "user",
		TargetID:      principal.UserId,
		TargetDisplay: principal.Email,
		Outcome:       auditlog.OutcomeSuccess,
	})
}

// recordLoginFailure records rejected credentials. Infrastructure failures
// (database down, missing ADMIN_EMAIL) are not sign-in attempts and stay out
// of the log.
func (a *DashboardAuthService) recordLoginFailure(ctx context.Context, email string, err error) {
	if a.onAuditEvent == nil || errors.Is(err, ErrAuthUnavailable) || errors.Is(err, ErrAdminEmailNotSet) {
		return
	}
	reason := "invalid_credentials"
	switch {
	case errors.Is(err, ErrPasswordLoginDisabledBySSO):
		reason = "sso_enforced"
	case errors.Is(err, ErrAccountPendingApproval):
		reason = "pending_approval"
	}
	a.onAuditEvent(ctx, auditlog.Event{
		ActorType:     auditlog.ActorUser,
		ActorDisplay:  email,
		Action:        auditlog.ActionUserLogin,
		TargetType:    "user",
		TargetDisplay: email,
		Outcome:       auditlog.OutcomeFailure,
		Metadata:      map[string]any{"reason": reason},
	})
}

// IssueSession mints the standard dashboard JWT pair for an account
// authenticated by other means than a password, such as the enterprise SSO
// callback.
func (a *DashboardAuthService) IssueSession(ctx context.Context, user store.User) (*DashboardSession, error) {
	if a.userRepo == nil {
		return nil, errors.New("sessions can only be issued for database-backed accounts")
	}
	principal, err := a.principalForUser(ctx, user)
	if err != nil {
		return nil, err
	}
	return a.startSession(ctx, *principal)
}

// ValidateSession accepts only a dashboard session JWT and returns who it was
// minted for, purely from the claims. It does not check the account still
// exists; AuthenticateSession does.
func (a *DashboardAuthService) ValidateSession(tokenString string) (*DashboardPrincipal, error) {
	claims := jwt.MapClaims{}
	_, err := crypto.DecodeAndExtractJWTToken(a.Secret, tokenString, &claims)
	if err != nil {
		return nil, err
	}
	if claims["type"] != "token" {
		return nil, errors.New("invalid token type")
	}
	if claims["sub"] != dashboardSubject {
		return nil, errors.New("invalid token subject")
	}
	principal := DashboardPrincipal{}
	if userId, ok := claims["userId"].(string); ok {
		principal.UserId = userId
	}
	if email, ok := claims["email"].(string); ok {
		principal.Email = email
	}
	if isAdmin, ok := claims["isAdmin"].(bool); ok {
		principal.IsAdmin = isAdmin
	}
	principal.SessionVersion = sessionVersionClaim(claims)
	return &principal, nil
}

// sessionVersionClaim reads the sv claim; a token minted before sv existed
// has none, which reads as generation 0.
func sessionVersionClaim(claims jwt.MapClaims) int32 {
	version, _ := claims["sv"].(float64)
	return int32(version)
}

// AuthenticateSession validates the token and re-reads the account behind it,
// so a session dies as soon as the account is deleted, disabled, or demoted.
func (a *DashboardAuthService) AuthenticateSession(ctx context.Context, tokenString string) (*DashboardPrincipal, error) {
	principal, err := a.ValidateSession(tokenString)
	if err != nil {
		return nil, err
	}
	if a.userRepo == nil {
		return principal, nil
	}
	if principal.UserId == "" {
		// A stateless-deployment token names no user and cannot authenticate here.
		return nil, ErrSessionRevoked
	}
	user, err := a.userRepo.GetUserByID(ctx, principal.UserId)
	if err != nil {
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			return nil, ErrSessionRevoked
		}
		return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
	}
	if !user.Enabled || user.SessionVersion != principal.SessionVersion {
		return nil, ErrSessionRevoked
	}
	return &DashboardPrincipal{
		UserId:         user.Id,
		Email:          user.Email,
		IsAdmin:        user.IsAdmin,
		SessionVersion: user.SessionVersion,
	}, nil
}

// RefreshSession trades a refresh token for a new pair and spends the old one.
// Step 2 runs before the spend so a database outage leaves the caller's token
// intact instead of burning a good credential over a blip.
func (a *DashboardAuthService) RefreshSession(ctx context.Context, tokenString string) (*DashboardSession, error) {
	claims := jwt.MapClaims{}
	if _, err := crypto.DecodeAndExtractJWTToken(a.Secret, tokenString, &claims); err != nil {
		return nil, err
	}
	if claims["type"] != "refreshToken" {
		return nil, errors.New("invalid token type")
	}
	if claims["sub"] != dashboardSubject {
		return nil, errors.New("invalid token subject")
	}

	if a.userRepo == nil {
		email, _ := claims["email"].(string)
		principal, err := resolveStatelessPrincipal(email, nil)
		if err != nil {
			return nil, err
		}
		return a.startSession(ctx, *principal)
	}

	userId, _ := claims["userId"].(string)
	principal, err := a.resolveRefreshPrincipal(ctx, userId)
	if err != nil {
		return nil, err
	}
	if principal.SessionVersion != sessionVersionClaim(claims) {
		return nil, ErrSessionRevoked
	}
	if a.refreshTokens == nil {
		return a.startSession(ctx, *principal)
	}
	spentId, _ := claims["jti"].(string)
	if spentId == "" {
		// Minted before rotation existed, or forged: no ledger row names it.
		return nil, ErrSessionRevoked
	}

	successorId := uuid.New().String()
	expiresAt := time.Now().Add(refreshTokenTTL)
	_, err = a.refreshTokens.RotateRefreshToken(ctx, store.RotateRefreshTokenParameters{
		OldID:     spentId,
		NewID:     successorId,
		ExpiresAt: expiresAt,
	})
	if err == nil {
		if purgeErr := a.refreshTokens.DeleteExpiredRefreshTokens(ctx, principal.UserId); purgeErr != nil {
			log.Printf("failed to purge expired refresh tokens for user %s: %v", principal.UserId, purgeErr)
		}
		return a.issueSessionPair(*principal, successorId, expiresAt)
	}
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	if !errors.As(err, &notFoundErr) {
		return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
	}

	// The rotation refused the token; find out why from its row.
	spent, err := a.refreshTokens.GetRefreshToken(ctx, spentId, refreshReplayGrace)
	if err != nil {
		if errors.As(err, &notFoundErr) {
			return nil, ErrSessionRevoked
		}
		return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
	}
	if spent.UsedAt == nil {
		return nil, ErrSessionRevoked
	}
	if spent.UsedRecently && spent.ReplacedBy != nil {
		// Same client racing itself within refreshReplayGrace: hand back the
		// successor already written instead of minting a second one.
		successor, err := a.refreshTokens.GetRefreshToken(ctx, *spent.ReplacedBy, refreshReplayGrace)
		if err != nil {
			if errors.As(err, &notFoundErr) {
				return nil, ErrSessionRevoked
			}
			return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
		}
		if successor.UsedAt != nil {
			return nil, ErrSessionRevoked
		}
		return a.issueSessionPair(*principal, successor.Id, successor.ExpiresAt)
	}

	// Spent long ago and presented again: a stolen credential. Bump the
	// account's generation to kill every session, not just this family.
	if err := a.refreshTokens.DeleteRefreshTokenFamily(ctx, spent.FamilyId); err != nil {
		log.Printf("failed to revoke refresh token family %s after a replay: %v", spent.FamilyId, err)
	}
	if err := a.userRepo.BumpUserSessionVersion(ctx, principal.UserId); err != nil {
		log.Printf("failed to revoke the sessions of user %s after a replay: %v", principal.UserId, err)
	}
	log.Printf("🚨 [AUTH] refresh token replay for user %s: family %s revoked, every session of the account invalidated", spent.UserId, spent.FamilyId)
	return nil, ErrRefreshTokenReuse
}
