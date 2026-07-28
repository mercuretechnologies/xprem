package services

import (
	"context"
	"errors"
	"expo-open-ota/internal/crypto"
	"expo-open-ota/internal/store"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revocationFixture is one control-plane deployment with an admin and a
// member, each holding a live session. Both are needed by nearly every case
// below: the admin performs the revocations, the member is the account whose
// sessions must die, and the admin's own session is what proves administrators
// are revoked on the same terms as members.
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

// The disable tests above would pass on the pre-existing `enabled` check
// alone, which is not the mechanism this ticket added. Re-enabling is what
// tells the two apart: once the account is enabled again, only a bumped
// session version still refuses the sessions it held while disabled. Those are
// exactly the sessions an admin revoked access to, usually because one of them
// was stolen.
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

	// The last enabled admin cannot be disabled, so promote the member first:
	// this is the case where the account losing its sessions is an admin.
	require.NoError(t, fixture.users.SetUserAdmin(ctx, fixture.admin.Id, fixture.member.Id, true))
	require.NoError(t, fixture.users.SetUserEnabled(ctx, fixture.member.Id, fixture.admin.Id, false))

	assertSessionDead(t, fixture.auth, fixture.adminSession)
}

func TestDemotingAnAdminRevokesItsLiveSession(t *testing.T) {
	fixture := newRevocationFixture(t)
	ctx := context.Background()

	require.NoError(t, fixture.users.SetUserAdmin(ctx, fixture.admin.Id, fixture.member.Id, true))
	require.NoError(t, fixture.users.SetUserAdmin(ctx, fixture.member.Id, fixture.admin.Id, false))

	// The demoted account keeps its password, so it may sign in again, as a
	// member. What it must not keep is the session minted while it was admin,
	// whose isAdmin claim would otherwise stand for another two hours.
	assertSessionDead(t, fixture.auth, fixture.adminSession)
	fresh, err := fixture.auth.LoginWithEmailPassword(ctx, fixture.admin.Email, "Sup3rSecret!")
	require.NoError(t, err)
	principal, err := fixture.auth.AuthenticateSession(ctx, fresh.Token)
	require.NoError(t, err)
	assert.False(t, principal.IsAdmin)
}

// Promoting is not a revocation: nothing about the account's existing session
// became untrustworthy, and signing someone out to grant them a flag would be
// gratuitous.
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

// Nothing else enforces the revocation a password change performs: no flag
// changes and the row stays enabled. A store that cannot record it must
// therefore fail the call, rather than leave the old sessions live behind a
// new password.
func TestChangingThePasswordFailsWhenSessionsCannotBeRevoked(t *testing.T) {
	fixture := newRevocationFixture(t)
	failing := &bumpFailingUserRepo{fakeUserRepo: fixture.repo}
	userService := NewUserService(failing)

	err := userService.ChangePassword(context.Background(), fixture.member.Id, "Sup3rSecret!", "An0therSecret!")
	assert.ErrorContains(t, err, "could not be revoked")
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

// A token minted by a stateless deployment names no account. Pointed at a
// control plane it must not authenticate the request as nobody.
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

	// Past the grace window, a second presentation is a leak, not the same
	// client asking twice.
	fixture.refreshTokens.markUsedAt(refreshTokenId(t, fixture.auth, stolen), time.Now().Add(-2*refreshReplayGrace))

	_, err = fixture.auth.RefreshSession(ctx, stolen)
	assert.ErrorIs(t, err, ErrRefreshTokenReuse)

	// Whichever of the two holders was the thief, neither keeps a refresh: the
	// successor handed to the legitimate client is gone too.
	_, err = fixture.auth.RefreshSession(ctx, rotated.RefreshToken)
	assert.ErrorIs(t, err, ErrSessionRevoked)

	// The account's OTHER sign-ins are a different family and survive.
	assertSessionAlive(t, fixture.auth, fixture.adminSession)
}

// A proven replay is proof that a credential of this account leaked, so the
// response is account-wide, not chain-wide: the access token the replay just
// produced belongs to no chain, and deleting the family alone would leave the
// party that replayed with a working dashboard for the rest of its two hours.
func TestReplayRevokesEverySessionOfTheAccount(t *testing.T) {
	fixture := newRevocationFixture(t)
	ctx := context.Background()

	otherDevice, err := fixture.auth.LoginWithEmailPassword(ctx, fixture.member.Email, "Sup3rSecret!")
	require.NoError(t, err)

	// The access token a thief would have pocketed by rotating the stolen
	// refresh token, taken here from the same account by the same means.
	stolen := fixture.memberSession.RefreshToken
	thiefSession, err := fixture.auth.RefreshSession(ctx, stolen)
	require.NoError(t, err)
	assertSessionAlive(t, fixture.auth, thiefSession)

	fixture.refreshTokens.markUsedAt(refreshTokenId(t, fixture.auth, stolen), time.Now().Add(-2*refreshReplayGrace))
	_, err = fixture.auth.RefreshSession(ctx, stolen)
	require.ErrorIs(t, err, ErrRefreshTokenReuse)

	// The access token minted from the compromised chain stops working at once.
	assertSessionDead(t, fixture.auth, thiefSession)
	// And so does the account's other device: on proof of a stolen credential,
	// signing the account out everywhere is the right trade.
	_, err = fixture.auth.RefreshSession(ctx, otherDevice.RefreshToken)
	assert.ErrorIs(t, err, ErrSessionRevoked)

	// Other ACCOUNTS are untouched.
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

	// Both callers get a usable session, and the chain is intact.
	assertSessionAlive(t, fixture.auth, first)
	assertSessionAlive(t, fixture.auth, second)

	// Load-bearing: the second caller is handed the successor the first one
	// already got, not a second live token. Two unconsumed tokens in a family
	// would mean neither holder ever presents a consumed one again, and the
	// chain would become permanently undetectable. Consuming it once must
	// therefore retire it for BOTH.
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

// A refresh token that reaches a client without a ledger row behind it is a
// credential nothing can rotate, detect a replay on, or revoke. Failing the
// sign-in is the only safe answer, so the ledger write is not best-effort.
func TestSignInFailsWhenTheLedgerCannotRecordTheToken(t *testing.T) {
	fixture := newRevocationFixture(t)
	fixture.refreshTokens.insertErr = errors.New("connection refused")

	_, err := fixture.auth.LoginWithEmailPassword(context.Background(), fixture.member.Email, "Sup3rSecret!")
	assert.ErrorIs(t, err, ErrAuthUnavailable)
}

// A rotation that fails on infrastructure must leave the presented token
// untouched. Burning it would cost the client its session over a blip, and the
// retry it would make later would then look like a replay and revoke the
// account.
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

// Sessions minted before rotation existed carry no jti. There is no row to
// retire, so there is no way to make them single-use: they are refused rather
// than grandfathered into a chain nothing can revoke.
func TestRefreshTokenWithoutALedgerRowIsRefused(t *testing.T) {
	fixture := newRevocationFixture(t)
	principal := DashboardPrincipal{UserId: fixture.member.Id, Email: fixture.member.Email}

	legacyToken, err := fixture.auth.generateRefreshToken(principal, "", time.Now().Add(refreshTokenTTL))
	require.NoError(t, err)

	_, err = fixture.auth.RefreshSession(context.Background(), *legacyToken)
	assert.ErrorIs(t, err, ErrSessionRevoked)
}

// Stateless deployments have no ledger, so their refresh token is reusable
// until it expires. That is the behaviour they have always had; the point here
// is that adding rotation did not break them.
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

// bumpFailingUserRepo accepts every write except the revocation itself.
type bumpFailingUserRepo struct {
	*fakeUserRepo
}

func (r *bumpFailingUserRepo) BumpUserSessionVersion(context.Context, string) error {
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
