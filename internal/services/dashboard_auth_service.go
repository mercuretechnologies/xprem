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
// short-lived session JWT and a long-lived refresh JWT, both minted after an
// email/password login. In control-plane mode the credentials are checked
// against the users table (userRepo); in stateless mode userRepo is nil and
// the single account comes from ADMIN_EMAIL/ADMIN_PASSWORD. It has no notion
// of apps — the credentials a CLI client presents for an app are a separate
// concern, see CliAuthService.
type DashboardAuthService struct {
	Secret   string
	userRepo UserRepository
	// refreshTokens is the rotation ledger: one row per refresh token issued,
	// which is what makes a refresh single-use and a replay detectable. Nil in
	// stateless mode, where there is no database to keep it in and the refresh
	// token stays the unrotated 7-day credential it has always been.
	refreshTokens RefreshTokenRepository
	// onAuditEvent is the audit emission seam; nil (community) means sign-ins
	// are not recorded. Only the password login path emits here — the refresh
	// path is session upkeep, not an authentication event.
	onAuditEvent auditlog.RecordFunc
	// ssoEnforced reports whether SSO is currently active (configured, enabled
	// and licensed). Injected by the enterprise wiring; nil means never
	// enforced, so the community edition is untouched. While enforced, member
	// accounts must sign in through SSO and only admins keep the password
	// login as a break-glass access.
	ssoEnforced func(context.Context) bool
}

// DashboardSession is the JWT pair handed to the dashboard on login or refresh.
type DashboardSession struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
}

// DashboardPrincipal identifies who is behind a validated dashboard session.
// UserId is empty in stateless mode, where the ADMIN_EMAIL account is not a
// database row and is always an admin.
type DashboardPrincipal struct {
	UserId  string
	Email   string
	IsAdmin bool
	// SessionVersion is the account's security generation at the time the
	// session was minted, mirrored into every token as the sv claim. Always 0
	// in stateless mode, which has no users table to bump.
	SessionVersion int32
}

// RefreshTokenRepository is the refresh-token ledger (see the field doc on
// DashboardAuthService). Like UserRepository it has no bucket implementation:
// rotation state only exists on the control plane.
type RefreshTokenRepository interface {
	InsertRefreshToken(ctx context.Context, params store.InsertRefreshTokenParameters) error
	// RotateRefreshToken retires one token and issues its successor in the same
	// family, atomically. store.ErrResourceNotFound means the presented token
	// was not claimable, which GetRefreshToken then explains: unknown token, or
	// one already rotated (a replay).
	RotateRefreshToken(ctx context.Context, params store.RotateRefreshTokenParameters) (store.RefreshToken, error)
	// GetRefreshToken answers with UsedRecently set relative to replayGrace,
	// decided by the store's own clock.
	GetRefreshToken(ctx context.Context, id string, replayGrace time.Duration) (store.RefreshToken, error)
	DeleteRefreshTokenFamily(ctx context.Context, familyId string) error
	DeleteExpiredRefreshTokens(ctx context.Context, userId string) error
}

// ErrAdminEmailNotSet is surfaced verbatim to the login response: an operator
// hitting it needs the instruction, not a generic 401.
var ErrAdminEmailNotSet = errors.New("ADMIN_EMAIL is not set: stateless mode logs into the dashboard with ADMIN_EMAIL and ADMIN_PASSWORD. Set ADMIN_EMAIL on the server and retry")

// ErrPasswordLoginDisabledBySSO is returned to member accounts that present a
// valid password while SSO is enforced. It is only surfaced after the
// password verified, so the actionable message never leaks account existence.
var ErrPasswordLoginDisabledBySSO = errors.New("password sign-in is disabled while SSO is active: sign in with SSO instead")

// ErrAccountPendingApproval is returned for an account whose enabled flag is
// off: either an admin revoked its access, or SSO manual validation is on and
// the account has not been approved yet. Like ErrPasswordLoginDisabledBySSO it
// is only reached after the credentials verified, so it leaks nothing about
// which accounts exist.
var ErrAccountPendingApproval = errors.New("this account is waiting for an administrator to approve it")

// dashboardSubject scopes every dashboard JWT to the dashboard itself. Both
// validators below reject any other subject, which is what keeps the upload
// tokens minted by localBucket — signed with the same JWT_SECRET — from being
// accepted here. Changing this value invalidates sessions already in the wild.
const dashboardSubject = "admin-dashboard"

// sessionTokenTTL is short on purpose: it bounds how long a session survives
// something the per-request check cannot see. refreshTokenTTL is how long a
// signed-in account may stay away before having to type its password again.
const (
	sessionTokenTTL = 2 * time.Hour
	refreshTokenTTL = 7 * 24 * time.Hour
)

// A refresh token works once. When the access token expires while the
// dashboard has several requests in flight, each of them tries to refresh
// with the same token: the first one gets a new pair, the others arrive after
// it is already spent. For this long, those late arrivals get that same new
// pair back instead of counting as a theft. 30 seconds covers a page load; a
// token that comes back later than that means someone else has a copy of it.
const refreshReplayGrace = 30 * time.Second

// ErrSessionRevoked is returned when a syntactically valid token no longer
// matches the account behind it: deleted, disabled, or its security generation
// moved on (demoted, password changed). Distinct from a malformed token so the
// middleware can answer 401 without confusing it with an infrastructure fault.
var ErrSessionRevoked = errors.New("this session is no longer valid: sign in again")

// ErrRefreshTokenReuse marks a refresh token presented after it was already
// rotated. Two parties hold the chain, so the whole family is revoked before
// this is returned.
var ErrRefreshTokenReuse = errors.New("this refresh token was already used: every session from that sign-in has been revoked")

// NewDashboardAuthService accepts nil repositories (stateless mode); logins
// are then checked against ADMIN_EMAIL/ADMIN_PASSWORD and refresh tokens are
// not rotated, having nowhere to record the rotation.
func NewDashboardAuthService(userRepo UserRepository, refreshTokens RefreshTokenRepository) *DashboardAuthService {
	return &DashboardAuthService{
		Secret:        config.GetEnv("JWT_SECRET"),
		userRepo:      userRepo,
		refreshTokens: refreshTokens,
	}
}

// SetSSOEnforced injects the live "SSO is active" signal (see the field doc).
func (a *DashboardAuthService) SetSSOEnforced(enforced func(context.Context) bool) {
	a.ssoEnforced = enforced
}

// generateSessionToken mints the access half. It carries isAdmin, but that
// snapshot is not what authorizes anything: AuthenticateSession re-reads the
// account on every request and overwrites it.
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

// generateRefreshToken carries only the account's identity, not its isAdmin
// snapshot: RefreshSession re-resolves the account so a revoked flag or a
// deleted user takes effect at the next refresh instead of surviving 7 days.
//
// tokenId names the ledger row that makes this token single-use. It is empty
// in stateless mode, where there is no ledger and the token stays reusable
// until it expires. The family is deliberately NOT a claim: it is read from
// the ledger row on every rotation, so nothing a holder presents can steer
// which chain a revocation walks.
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
// already been written (or, in stateless mode, for no row at all). Persisting
// first and signing second is what keeps a token from ever reaching a client
// without a row that can revoke it.
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
		// Housekeeping, bounded to this account. Not a reason to refuse a valid
		// sign-in.
		log.Printf("failed to purge expired refresh tokens for user %s: %v", principal.UserId, err)
	}
	return a.issueSessionPair(principal, tokenId, expiresAt)
}

// resolveStatelessPrincipal checks credentials against ADMIN_EMAIL and
// ADMIN_PASSWORD. When password is nil only the account's existence is
// resolved (the refresh path, where possession of the JWT is the credential).
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
// names an email with no account, so unknown and known emails cost the same
// bcrypt comparison and response timing cannot enumerate accounts.
const unknownUserPasswordHash = "$2a$10$RTxsxJsH5d9yZcM.fDe/kOv28rciQYAnNBOrK0frmWJPZGH1pTzhO"

// ErrAuthUnavailable marks login/refresh failures caused by the account store
// being unreachable — an infrastructure problem the handlers must surface as
// a 500, never as invalid credentials.
var ErrAuthUnavailable = errors.New("could not verify the account against the database")

// principalForUser records the connection and builds the session principal.
// Both a password login and a refresh prove the account is actively using the
// dashboard. Best-effort: a failed touch must not fail the sign-in.
// principalForUser is the single choke point every database-backed sign-in
// path goes through (password login, SSO callback, token refresh), which is
// why the enabled check lives here: no path can acquire a principal without
// passing it. On the refresh path this is also what makes disabling an account
// effective, since a live session token is never re-read against the database.
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
			// Unknown account: burn the same bcrypt cost as a real comparison
			// so response timing cannot enumerate which emails exist.
			crypto.VerifyPassword(unknownUserPasswordHash, password)
			return nil, errors.New("invalid credentials")
		}
		return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
	}
	if user.PasswordHash == "" {
		// SSO-provisioned accounts carry an empty hash and can never sign in
		// with a password. bcrypt would reject "" instantly, which would make
		// them enumerable by timing; burn the same cost as any wrong password.
		crypto.VerifyPassword(unknownUserPasswordHash, password)
		return nil, errors.New("invalid credentials")
	}
	if !crypto.VerifyPassword(user.PasswordHash, password) {
		return nil, errors.New("invalid credentials")
	}
	// Checked only after the password verified, so wrong-password responses
	// keep a uniform timing and message whether SSO is enforced or not.
	if a.ssoEnforced != nil && a.ssoEnforced(ctx) && !user.IsAdmin {
		return nil, ErrPasswordLoginDisabledBySSO
	}
	return a.principalForUser(ctx, user)
}

// resolveRefreshPrincipal re-resolves the account behind a refresh token by
// its immutable user id — never by email: a deleted account whose address is
// later reused must not let the old refresh token resurrect into the new one.
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

// SetOnAuditEvent plugs the audit emission seam (see SetSSOEnforced for the
// pattern; the enterprise wiring passes ee/audit's Record method value).
// Nil-safe: without it, sign-ins simply leave no audit events.
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

// recordLoginFailure records rejected credentials, the brute-force signal a
// security review asks for. Infrastructure failures (database down, missing
// ADMIN_EMAIL) are not sign-in attempts and stay out of the log.
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
	// The account may not exist: the attempted email is the only identity a
	// failure can carry, so the actor id stays empty.
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

// IssueSession mints the standard dashboard JWT pair for an account that was
// authenticated by other means than a password (the enterprise SSO callback).
// It only exists in control-plane mode, where user is always a database row.
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

// ValidateSession accepts only a dashboard session JWT — not a refresh token,
// and not any other JWT signed with the same secret — and returns who it was
// minted for, purely from the claims. It says the token is authentic, not that
// the account behind it still exists: authenticating a request goes through
// AuthenticateSession, which is the only caller outside the tests.
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

// sessionVersionClaim reads the sv claim. JSON numbers decode as float64, and
// a token minted before sv existed simply has none: both read as generation 0,
// which is the default every existing row carries.
func sessionVersionClaim(claims jwt.MapClaims) int32 {
	version, _ := claims["sv"].(float64)
	return int32(version)
}

// AuthenticateSession is what an authenticated request goes through: it
// validates the token AND re-reads the account behind it, so a session dies as
// soon as the account is deleted, disabled, or has its security generation
// bumped (demoted, password changed). It costs one users read per request,
// which is the price of not having sessions that outlive the account by up to
// two hours.
//
// The principal it returns carries the account's CURRENT admin flag, read from
// the row rather than from the token, so nothing downstream has to distrust it.
// In stateless mode there is no users table and the claims stand alone.
func (a *DashboardAuthService) AuthenticateSession(ctx context.Context, tokenString string) (*DashboardPrincipal, error) {
	principal, err := a.ValidateSession(tokenString)
	if err != nil {
		return nil, err
	}
	if a.userRepo == nil {
		return principal, nil
	}
	// A token minted by a stateless deployment names no user. It cannot
	// identify an account here, and letting it through would authenticate a
	// request as nobody.
	if principal.UserId == "" {
		return nil, ErrSessionRevoked
	}
	user, err := a.userRepo.GetUserByID(ctx, principal.UserId)
	if err != nil {
		// Only a missing row means the account is gone; an infrastructure
		// failure must not read as a dead session, or a database blip would
		// sign everyone out.
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
// The whole flow is here, in order, because it is one story:
//
//	1. Is the token ours, and is it a refresh token?
//	2. Does the account still exist, is it still enabled, and are these
//	   credentials still current? A deleted, disabled, demoted or
//	   password-changed account cannot refresh its way back in.
//	3. Spend the token and write its successor, in one transaction.
//	4. If it could not be spent, find out why. Three answers mean "sign in
//	   again", one means "this same client already asked", and one means the
//	   token leaked.
//
// Step 2 runs before step 3 so a database outage leaves the caller's token
// intact. Spending it first would burn a good credential over a blip, and the
// retry it makes later would then look like the leak in step 4.
func (a *DashboardAuthService) RefreshSession(ctx context.Context, tokenString string) (*DashboardSession, error) {
	// 1. The token itself.
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

	// Stateless mode has no users table and no ledger, so there is nothing to
	// re-check and nothing to rotate. The token buys a new pair until it
	// expires, which is the behaviour it has always had.
	if a.userRepo == nil {
		email, _ := claims["email"].(string)
		principal, err := resolveStatelessPrincipal(email, nil)
		if err != nil {
			return nil, err
		}
		return a.startSession(ctx, *principal)
	}

	// 2. The account behind it, read fresh.
	userId, _ := claims["userId"].(string)
	principal, err := a.resolveRefreshPrincipal(ctx, userId)
	if err != nil {
		return nil, err
	}
	if principal.SessionVersion != sessionVersionClaim(claims) {
		// The account changed under this token: demoted, password changed, or
		// an admin revoked its sessions.
		return nil, ErrSessionRevoked
	}
	if a.refreshTokens == nil {
		return a.startSession(ctx, *principal)
	}
	spentId, _ := claims["jti"].(string)
	if spentId == "" {
		// Minted before rotation existed, or forged. With no ledger row naming
		// it, there is no way to make this token single-use.
		return nil, ErrSessionRevoked
	}

	// 3. Spend it. The successor is written in the same transaction, so this
	// either fully happens or not at all.
	successorId := uuid.New().String()
	expiresAt := time.Now().Add(refreshTokenTTL)
	_, err = a.refreshTokens.RotateRefreshToken(ctx, store.RotateRefreshTokenParameters{
		OldID:     spentId,
		NewID:     successorId,
		ExpiresAt: expiresAt,
	})
	if err == nil {
		if purgeErr := a.refreshTokens.DeleteExpiredRefreshTokens(ctx, principal.UserId); purgeErr != nil {
			// Housekeeping, bounded to this account. Not a reason to refuse.
			log.Printf("failed to purge expired refresh tokens for user %s: %v", principal.UserId, purgeErr)
		}
		return a.issueSessionPair(*principal, successorId, expiresAt)
	}
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	if !errors.As(err, &notFoundErr) {
		return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
	}

	// 4. It would not spend. The row says why.
	spent, err := a.refreshTokens.GetRefreshToken(ctx, spentId, refreshReplayGrace)
	if err != nil {
		if errors.As(err, &notFoundErr) {
			// No such row: the family was revoked, or the id was invented.
			return nil, ErrSessionRevoked
		}
		return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
	}
	if spent.UsedAt == nil {
		// Never spent, yet the rotation refused it: the only other thing it
		// checks is the expiry.
		return nil, ErrSessionRevoked
	}
	if spent.UsedRecently && spent.ReplacedBy != nil {
		// The same client asking twice, its parallel requests having all hit a
		// 401 at once (see refreshReplayGrace). Give it the successor already
		// written rather than a second one: two live tokens in one family is a
		// fork, and a fork can never be caught replaying below.
		successor, err := a.refreshTokens.GetRefreshToken(ctx, *spent.ReplacedBy, refreshReplayGrace)
		if err != nil {
			if errors.As(err, &notFoundErr) {
				return nil, ErrSessionRevoked
			}
			return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
		}
		if successor.UsedAt != nil {
			// The successor has itself been spent since. Which link the caller
			// should hold is now guesswork; one sign-in is cheaper than a wrong
			// guess.
			return nil, ErrSessionRevoked
		}
		return a.issueSessionPair(*principal, successor.Id, successor.ExpiresAt)
	}

	// Spent long ago and presented again: two parties hold this chain.
	// Deleting the family is not enough, because the access token the replay
	// produced belongs to no family. Bumping the account's generation is what
	// kills it, and it signs the account out everywhere. On proof of a stolen
	// credential that is the right trade, and it is what a demotion does too.
	if err := a.refreshTokens.DeleteRefreshTokenFamily(ctx, spent.FamilyId); err != nil {
		log.Printf("failed to revoke refresh token family %s after a replay: %v", spent.FamilyId, err)
	}
	if err := a.userRepo.BumpUserSessionVersion(ctx, principal.UserId); err != nil {
		log.Printf("failed to revoke the sessions of user %s after a replay: %v", principal.UserId, err)
	}
	log.Printf("🚨 [AUTH] refresh token replay for user %s: family %s revoked, every session of the account invalidated", spent.UserId, spent.FamilyId)
	return nil, ErrRefreshTokenReuse
}
