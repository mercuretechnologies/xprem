package services

import (
	"context"
	"errors"
	"testing"
	"time"
	"xprem/internal/crypto"
	"xprem/internal/store"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revocationFixture is one control-plane deployment with an admin and a
// member, each holding a live session.
type revocationFixture struct {
	auth          *DashboardAuthService
	users         *UserService
	repo          *fakeUserRepo
	refreshTokens *fakeRefreshTokenRepo
	admin         store.User
	member        store.User
	adminSession  *DashboardSession
	memberSession *DashboardSession
}

func newRevocationFixture(t *testing.T) *revocationFixture {
	t.Helper()
	t.Setenv("JWT_SECRET", "test-secret")
	ctx := context.Background()
	repo := newFakeUserRepo()
	refreshTokens := newFakeRefreshTokenRepo()
	userService := NewUserService(repo)
	authService := NewDashboardAuthService(repo, refreshTokens)

	admin, err := userService.CreateUser(ctx, "admin@example.com", "Sup3rSecret!", true)
	require.NoError(t, err)
	member, err := userService.CreateUser(ctx, "member@example.com", "Sup3rSecret!", false)
	require.NoError(t, err)

	adminSession, err := authService.LoginWithEmailPassword(ctx, admin.Email, "Sup3rSecret!")
	require.NoError(t, err)
	memberSession, err := authService.LoginWithEmailPassword(ctx, member.Email, "Sup3rSecret!")
	require.NoError(t, err)

	return &revocationFixture{
		auth:          authService,
		users:         userService,
		repo:          repo,
		refreshTokens: refreshTokens,
		admin:         admin,
		member:        member,
		adminSession:  adminSession,
		memberSession: memberSession,
	}
}

// assertSessionDead is the whole point of the ticket in one assertion: neither
// half of the pair may still be worth anything.
func assertSessionDead(t *testing.T, auth *DashboardAuthService, session *DashboardSession) {
	t.Helper()
	ctx := context.Background()
	_, err := auth.AuthenticateSession(ctx, session.Token)
	assert.ErrorIs(t, err, ErrSessionRevoked, "the access token should have been revoked")
	_, err = auth.RefreshSession(ctx, session.RefreshToken)
	assert.Error(t, err, "the refresh token should have been revoked")
}

func assertSessionAlive(t *testing.T, auth *DashboardAuthService, session *DashboardSession) {
	t.Helper()
	_, err := auth.AuthenticateSession(context.Background(), session.Token)
	assert.NoError(t, err)
}

// refreshTokenId reads the jti a refresh token names, so a test can reach the
// ledger row behind a token it only holds as a string.
func refreshTokenId(t *testing.T, auth *DashboardAuthService, refreshToken string) string {
	t.Helper()
	claims := jwt.MapClaims{}
	_, err := crypto.DecodeAndExtractJWTToken(auth.Secret, refreshToken, &claims)
	require.NoError(t, err)
	id, _ := claims["jti"].(string)
	require.NotEmpty(t, id, "a refresh token issued on the control plane must name its ledger row")
	return id
}

func TestDisablingAMemberRevokesItsLiveSession(t *testing.T) {
	fixture := newRevocationFixture(t)
	ctx := context.Background()

	assertSessionAlive(t, fixture.auth, fixture.memberSession)
	require.NoError(t, fixture.users.SetUserEnabled(ctx, fixture.admin.Id, fixture.member.Id, false))

	assertSessionDead(t, fixture.auth, fixture.memberSession)
	// The admin who performed the revocation is untouched by it.
	assertSessionAlive(t, fixture.auth, fixture.adminSession)
}

// Re-enabling the account must not resurrect the sessions it held while disabled.
func TestReEnablingAnAccountDoesNotResurrectItsOldSessions(t *testing.T) {
	fixture := newRevocationFixture(t)
	ctx := context.Background()

	require.NoError(t, fixture.users.SetUserEnabled(ctx, fixture.admin.Id, fixture.member.Id, false))
	require.NoError(t, fixture.users.SetUserEnabled(ctx, fixture.admin.Id, fixture.member.Id, true))

	assertSessionDead(t, fixture.auth, fixture.memberSession)
	// The account itself is usable again, it just needs to sign in.
	_, err := fixture.auth.LoginWithEmailPassword(ctx, fixture.member.Email, "Sup3rSecret!")
	assert.NoError(t, err)
}

func TestDisablingAnAdminRevokesItsLiveSession(t *testing.T) {
	fixture := newRevocationFixture(t)
	ctx := context.Background()

	// The last enabled admin cannot be disabled, so promote the member first.
	require.NoError(t, fixture.users.SetUserAdmin(ctx, fixture.admin.Id, fixture.member.Id, true))
	require.NoError(t, fixture.users.SetUserEnabled(ctx, fixture.member.Id, fixture.admin.Id, false))

	assertSessionDead(t, fixture.auth, fixture.adminSession)
}

func TestDemotingAnAdminRevokesItsLiveSession(t *testing.T) {
	fixture := newRevocationFixture(t)
	ctx := context.Background()

	require.NoError(t, fixture.users.SetUserAdmin(ctx, fixture.admin.Id, fixture.member.Id, true))
	require.NoError(t, fixture.users.SetUserAdmin(ctx, fixture.member.Id, fixture.admin.Id, false))

	// The demoted account may sign in again, but not on its old isAdmin session.
	assertSessionDead(t, fixture.auth, fixture.adminSession)
	fresh, err := fixture.auth.LoginWithEmailPassword(ctx, fixture.admin.Email, "Sup3rSecret!")
	require.NoError(t, err)
	principal, err := fixture.auth.AuthenticateSession(ctx, fresh.Token)
	require.NoError(t, err)
	assert.False(t, principal.IsAdmin)
}

// Promoting is not a revocation.
func TestPromotingAMemberLeavesItsSessionAlive(t *testing.T) {
	fixture := newRevocationFixture(t)

	require.NoError(t, fixture.users.SetUserAdmin(context.Background(), fixture.admin.Id, fixture.member.Id, true))

	principal, err := fixture.auth.AuthenticateSession(context.Background(), fixture.memberSession.Token)
	require.NoError(t, err)
	// The flag comes from the row, not from the claim minted before the grant.
	assert.True(t, principal.IsAdmin)
}

func TestChangingThePasswordRevokesEverySessionOfTheAccount(t *testing.T) {
	fixture := newRevocationFixture(t)
	ctx := context.Background()

	// A second sign-in: the forgotten laptop, or the attacker's session.
	otherSession, err := fixture.auth.LoginWithEmailPassword(ctx, fixture.member.Email, "Sup3rSecret!")
	require.NoError(t, err)

	require.NoError(t, fixture.users.ChangePassword(ctx, fixture.member.Id, "Sup3rSecret!", "An0therSecret!"))

	assertSessionDead(t, fixture.auth, fixture.memberSession)
	assertSessionDead(t, fixture.auth, otherSession)
}

// The password write and the session bump happen in one write; a failure
// must leave neither applied.
func TestAFailedPasswordWriteRevokesNothing(t *testing.T) {
	fixture := newRevocationFixture(t)
	failing := &passwordWriteFailingUserRepo{fakeUserRepo: fixture.repo}

	err := NewUserService(failing).ChangePassword(
		context.Background(), fixture.member.Id, "Sup3rSecret!", "An0therSecret!")
	require.Error(t, err)

	stored, getErr := fixture.repo.GetUserByID(context.Background(), fixture.member.Id)
	require.NoError(t, getErr)
	assert.EqualValues(t, 0, stored.SessionVersion, "no password, no revocation")
	assertSessionAlive(t, fixture.auth, fixture.memberSession)
}

func TestDeletingAnAccountRevokesItsLiveSession(t *testing.T) {
	fixture := newRevocationFixture(t)

	require.NoError(t, fixture.users.DeleteUser(context.Background(), fixture.admin.Id, fixture.member.Id))

	assertSessionDead(t, fixture.auth, fixture.memberSession)
}

// An outage while resolving the account is not a revocation: reading it as one
// would sign every account out over a database blip.
func TestAuthenticateSessionSeparatesAnOutageFromARevocation(t *testing.T) {
	fixture := newRevocationFixture(t)
	unavailable := NewDashboardAuthService(&unavailableUserRepo{}, fixture.refreshTokens)
	unavailable.Secret = fixture.auth.Secret

	_, err := unavailable.AuthenticateSession(context.Background(), fixture.memberSession.Token)
	assert.ErrorIs(t, err, ErrAuthUnavailable)
	assert.NotErrorIs(t, err, ErrSessionRevoked)
}

// A token minted by a stateless deployment names no account and must not
// authenticate a request against a control plane as nobody.
func TestAuthenticateSessionRejectsATokenWithNoAccount(t *testing.T) {
	fixture := newRevocationFixture(t)
	t.Setenv("ADMIN_EMAIL", "admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "admin")

	stateless := NewDashboardAuthService(nil, nil)
	statelessSession, err := stateless.LoginWithEmailPassword(context.Background(), "admin@example.com", "admin")
	require.NoError(t, err)

	_, err = fixture.auth.AuthenticateSession(context.Background(), statelessSession.Token)
	assert.ErrorIs(t, err, ErrSessionRevoked)
}

func TestRefreshRotatesTheTokenItWasGiven(t *testing.T) {
	fixture := newRevocationFixture(t)
	ctx := context.Background()

	rotated, err := fixture.auth.RefreshSession(ctx, fixture.memberSession.RefreshToken)
	require.NoError(t, err)
	assert.NotEqual(t, fixture.memberSession.RefreshToken, rotated.RefreshToken)

	// The successor works, and stays in the same family as its predecessor so
	// the chain can be revoked as a unit.
	previous, err := fixture.refreshTokens.GetRefreshToken(ctx, refreshTokenId(t, fixture.auth, fixture.memberSession.RefreshToken), refreshReplayGrace)
	require.NoError(t, err)
	successor, err := fixture.refreshTokens.GetRefreshToken(ctx, refreshTokenId(t, fixture.auth, rotated.RefreshToken), refreshReplayGrace)
	require.NoError(t, err)
	assert.Equal(t, previous.FamilyId, successor.FamilyId)
	assert.NotNil(t, previous.UsedAt, "the presented token should have been retired")

	_, err = fixture.auth.RefreshSession(ctx, rotated.RefreshToken)
	assert.NoError(t, err)
}

func TestReplayingARotatedRefreshTokenRevokesTheWholeChain(t *testing.T) {
	fixture := newRevocationFixture(t)
	ctx := context.Background()

	stolen := fixture.memberSession.RefreshToken
	rotated, err := fixture.auth.RefreshSession(ctx, stolen)
	require.NoError(t, err)

	// Past the grace window, a second presentation is a leak, not a race.
	fixture.refreshTokens.markUsedAt(refreshTokenId(t, fixture.auth, stolen), time.Now().Add(-2*refreshReplayGrace))

	_, err = fixture.auth.RefreshSession(ctx, stolen)
	assert.ErrorIs(t, err, ErrRefreshTokenReuse)

	// Neither holder keeps a refresh, including the successor already issued.
	_, err = fixture.auth.RefreshSession(ctx, rotated.RefreshToken)
	assert.ErrorIs(t, err, ErrSessionRevoked)

	// The account's other sign-ins are a different family and survive.
	assertSessionAlive(t, fixture.auth, fixture.adminSession)
}

// A proven replay revokes the whole account, not just the compromised chain.
func TestReplayRevokesEverySessionOfTheAccount(t *testing.T) {
	fixture := newRevocationFixture(t)
	ctx := context.Background()

	otherDevice, err := fixture.auth.LoginWithEmailPassword(ctx, fixture.member.Email, "Sup3rSecret!")
	require.NoError(t, err)

	// The access token a thief would have pocketed by rotating the stolen token.
	stolen := fixture.memberSession.RefreshToken
	thiefSession, err := fixture.auth.RefreshSession(ctx, stolen)
	require.NoError(t, err)
	assertSessionAlive(t, fixture.auth, thiefSession)

	fixture.refreshTokens.markUsedAt(refreshTokenId(t, fixture.auth, stolen), time.Now().Add(-2*refreshReplayGrace))
	_, err = fixture.auth.RefreshSession(ctx, stolen)
	require.ErrorIs(t, err, ErrRefreshTokenReuse)

	assertSessionDead(t, fixture.auth, thiefSession)
	// The account's other device is signed out too.
	_, err = fixture.auth.RefreshSession(ctx, otherDevice.RefreshToken)
	assert.ErrorIs(t, err, ErrSessionRevoked)

	// Other accounts are untouched.
	assertSessionAlive(t, fixture.auth, fixture.adminSession)
}

// The dashboard fires requests in parallel, so one expiring access token
// produces several refreshes carrying the same token. That is not a leak.
func TestConcurrentRefreshWithinTheGraceWindowIsNotAReplay(t *testing.T) {
	fixture := newRevocationFixture(t)
	ctx := context.Background()

	first, err := fixture.auth.RefreshSession(ctx, fixture.memberSession.RefreshToken)
	require.NoError(t, err)
	second, err := fixture.auth.RefreshSession(ctx, fixture.memberSession.RefreshToken)
	require.NoError(t, err)

	assertSessionAlive(t, fixture.auth, first)
	assertSessionAlive(t, fixture.auth, second)

	// The second caller must be handed the same successor as the first, not a
	// second live token, or the chain becomes permanently undetectable.
	assert.Equal(t,
		refreshTokenId(t, fixture.auth, first.RefreshToken),
		refreshTokenId(t, fixture.auth, second.RefreshToken),
		"a replay inside the grace window must not fork the chain")

	_, err = fixture.auth.RefreshSession(ctx, second.RefreshToken)
	require.NoError(t, err)
	fixture.refreshTokens.markUsedAt(refreshTokenId(t, fixture.auth, first.RefreshToken), time.Now().Add(-2*refreshReplayGrace))
	_, err = fixture.auth.RefreshSession(ctx, first.RefreshToken)
	assert.ErrorIs(t, err, ErrRefreshTokenReuse, "detection must still work on the forked-into token")
}

// The ledger write is not best-effort: a token reaching the client without a
// ledger row could never be rotated, replay-detected, or revoked.
func TestSignInFailsWhenTheLedgerCannotRecordTheToken(t *testing.T) {
	fixture := newRevocationFixture(t)
	fixture.refreshTokens.insertErr = errors.New("connection refused")

	_, err := fixture.auth.LoginWithEmailPassword(context.Background(), fixture.member.Email, "Sup3rSecret!")
	assert.ErrorIs(t, err, ErrAuthUnavailable)
}

// A rotation that fails on infrastructure must leave the presented token
// untouched, or the client's retry would look like a replay.
func TestRotationOutageDoesNotBurnTheRefreshToken(t *testing.T) {
	fixture := newRevocationFixture(t)
	ctx := context.Background()

	fixture.refreshTokens.rotateErr = errors.New("connection refused")
	_, err := fixture.auth.RefreshSession(ctx, fixture.memberSession.RefreshToken)
	assert.ErrorIs(t, err, ErrAuthUnavailable)
	assert.NotErrorIs(t, err, ErrRefreshTokenReuse)

	fixture.refreshTokens.rotateErr = nil
	_, err = fixture.auth.RefreshSession(ctx, fixture.memberSession.RefreshToken)
	assert.NoError(t, err, "the token must survive the outage")
}

// Sessions minted before rotation existed carry no jti and no ledger row, so
// they are refused rather than grandfathered in.
func TestRefreshTokenWithoutALedgerRowIsRefused(t *testing.T) {
	fixture := newRevocationFixture(t)
	principal := DashboardPrincipal{UserId: fixture.member.Id, Email: fixture.member.Email}

	legacyToken, err := fixture.auth.generateRefreshToken(principal, "", time.Now().Add(refreshTokenTTL))
	require.NoError(t, err)

	_, err = fixture.auth.RefreshSession(context.Background(), *legacyToken)
	assert.ErrorIs(t, err, ErrSessionRevoked)
}

// Stateless deployments have no ledger, so their refresh token stays reusable
// until it expires.
func TestStatelessRefreshStillWorksWithoutALedger(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("ADMIN_EMAIL", "admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "admin")
	authService := NewDashboardAuthService(nil, nil)
	ctx := context.Background()

	session, err := authService.LoginWithEmailPassword(ctx, "admin@example.com", "admin")
	require.NoError(t, err)
	_, err = authService.RefreshSession(ctx, session.RefreshToken)
	require.NoError(t, err)
	_, err = authService.RefreshSession(ctx, session.RefreshToken)
	assert.NoError(t, err)
}

// passwordWriteFailingUserRepo refuses the one statement that carries both the
// new password and the revocation.
type passwordWriteFailingUserRepo struct {
	*fakeUserRepo
}

func (r *passwordWriteFailingUserRepo) UpdateUserPassword(context.Context, string, string) error {
	return errors.New("database is on fire")
}

// unavailableUserRepo stands for a database that is down: every read fails
// with something that is not a missing row.
type unavailableUserRepo struct {
	*fakeUserRepo
}

func (r *unavailableUserRepo) GetUserByID(context.Context, string) (store.User, error) {
	return store.User{}, errors.New("connection refused")
}

// Changing ADMIN_PASSWORD is the only revocation lever a stateless deployment
// has: nothing names the session in a ledger, and the signing key is not
// derived from the password.
func TestStatelessSessionDiesWhenAdminPasswordChanges(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("ADMIN_EMAIL", "admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "admin")
	authService := NewDashboardAuthService(nil, nil)
	ctx := context.Background()

	session, err := authService.LoginWithEmailPassword(ctx, "admin@example.com", "admin")
	require.NoError(t, err)

	t.Setenv("ADMIN_PASSWORD", "rotated")

	_, err = authService.RefreshSession(ctx, session.RefreshToken)
	assert.ErrorIs(t, err, ErrSessionRevoked, "a stolen refresh token must not outlive the password it was minted under")
	_, err = authService.AuthenticateSession(ctx, session.Token)
	assert.ErrorIs(t, err, ErrSessionRevoked, "nor must the access token handed out with it")
}
