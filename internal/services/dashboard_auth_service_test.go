package services

import (
	"context"
	"testing"
	"time"
	"xprem/internal/crypto"
	"xprem/internal/store"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seededSSOUser mirrors what the enterprise SSO provisioning inserts: a
// member account with the empty password-hash sentinel.
func seededSSOUser(id string, email string) store.InsertUserParameters {
	return store.InsertUserParameters{ID: id, Email: email, PasswordHash: "", IsAdmin: false, Enabled: true}
}

// SSO-provisioned accounts carry an empty password hash: no password may ever
// verify against them, whatever the input.
func TestPasswordLoginRejectsEmptyHashAccounts(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	repo := newFakeUserRepo()
	authService := NewDashboardAuthService(repo, newFakeRefreshTokenRepo())
	ctx := context.Background()

	_, err := repo.InsertUser(ctx, seededSSOUser("sso-user", "sso-member@example.com"))
	require.NoError(t, err)

	_, err = authService.LoginWithEmailPassword(ctx, "sso-member@example.com", "")
	assert.ErrorContains(t, err, "invalid credentials")
	_, err = authService.LoginWithEmailPassword(ctx, "sso-member@example.com", "AnyPassw0rd!")
	assert.ErrorContains(t, err, "invalid credentials")
}

func TestIssueSessionMintsAValidPair(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	repo := newFakeUserRepo()
	authService := NewDashboardAuthService(repo, newFakeRefreshTokenRepo())
	ctx := context.Background()

	user, err := repo.InsertUser(ctx, seededSSOUser("sso-user", "sso-member@example.com"))
	require.NoError(t, err)

	session, err := authService.IssueSession(ctx, user)
	require.NoError(t, err)

	principal, err := authService.ValidateSession(session.Token)
	require.NoError(t, err)
	assert.Equal(t, user.Id, principal.UserId)
	assert.Equal(t, user.Email, principal.Email)
	assert.False(t, principal.IsAdmin)

	// The refresh half behaves like any dashboard session.
	_, err = authService.RefreshSession(ctx, session.RefreshToken)
	require.NoError(t, err)

	// Issuing a session records the connection, like a password login does.
	stored, err := repo.GetUserByID(ctx, user.Id)
	require.NoError(t, err)
	assert.NotNil(t, stored.LastConnectedAt)

	// Stateless mode has no database accounts to issue sessions for.
	statelessService := NewDashboardAuthService(nil, nil)
	_, err = statelessService.IssueSession(ctx, user)
	assert.Error(t, err)
}

// Disabling an account must close every door, not just the login form. The
// refresh path matters most: session tokens are validated from their claims
// alone, so a revoked account keeps a working session until it refreshes.
func TestDisabledAccountCannotSignInOrRefresh(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	repo := newFakeUserRepo()
	userService := NewUserService(repo)
	authService := NewDashboardAuthService(repo, newFakeRefreshTokenRepo())
	ctx := context.Background()

	admin, err := userService.CreateUser(ctx, "admin@example.com", "Sup3rSecret!", true)
	require.NoError(t, err)
	member, err := userService.CreateUser(ctx, "member@example.com", "Sup3rSecret!", false)
	require.NoError(t, err)

	// A live session, obtained while the account was still enabled.
	session, err := authService.LoginWithEmailPassword(ctx, "member@example.com", "Sup3rSecret!")
	require.NoError(t, err)

	require.NoError(t, userService.SetUserEnabled(ctx, admin.Id, member.Id, false))

	// Password login: the credentials are right, the account is not usable.
	_, err = authService.LoginWithEmailPassword(ctx, "member@example.com", "Sup3rSecret!")
	assert.ErrorIs(t, err, ErrAccountPendingApproval)

	// Refresh: the leftover refresh token cannot mint a new pair.
	_, err = authService.RefreshSession(ctx, session.RefreshToken)
	assert.ErrorIs(t, err, ErrAccountPendingApproval)

	// SSO callback path: same refusal, so no flow can bypass the flag.
	disabled, err := repo.GetUserByID(ctx, member.Id)
	require.NoError(t, err)
	_, err = authService.IssueSession(ctx, disabled)
	assert.ErrorIs(t, err, ErrAccountPendingApproval)

	// Re-approving restores access.
	require.NoError(t, userService.SetUserEnabled(ctx, admin.Id, member.Id, true))
	_, err = authService.LoginWithEmailPassword(ctx, "member@example.com", "Sup3rSecret!")
	assert.NoError(t, err)
}

func TestSSOEnforcementOnPasswordLogin(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	repo := newFakeUserRepo()
	userService := NewUserService(repo)
	authService := NewDashboardAuthService(repo, newFakeRefreshTokenRepo())
	ctx := context.Background()

	_, err := userService.CreateUser(ctx, "admin@example.com", "Sup3rSecret!", true)
	require.NoError(t, err)
	_, err = userService.CreateUser(ctx, "member@example.com", "Sup3rSecret!", false)
	require.NoError(t, err)

	memberSession, err := authService.LoginWithEmailPassword(ctx, "member@example.com", "Sup3rSecret!")
	require.NoError(t, err)

	ssoActive := true
	authService.SetSSOEnforced(func(context.Context) bool { return ssoActive })

	// Members with a correct password are redirected to SSO; a wrong password
	// stays a plain 401-style failure so account state never leaks.
	_, err = authService.LoginWithEmailPassword(ctx, "member@example.com", "Sup3rSecret!")
	assert.ErrorIs(t, err, ErrPasswordLoginDisabledBySSO)
	_, err = authService.LoginWithEmailPassword(ctx, "member@example.com", "wrong-password")
	assert.ErrorContains(t, err, "invalid credentials")

	// Admins keep the password login as break-glass access.
	_, err = authService.LoginWithEmailPassword(ctx, "admin@example.com", "Sup3rSecret!")
	assert.NoError(t, err)

	// Refresh is never blocked: SSO-minted member sessions must keep working
	// and session origin is indistinguishable anyway.
	_, err = authService.RefreshSession(ctx, memberSession.RefreshToken)
	assert.NoError(t, err)

	// SSO turned off again: everything is back to normal for everyone.
	ssoActive = false
	_, err = authService.LoginWithEmailPassword(ctx, "member@example.com", "Sup3rSecret!")
	assert.NoError(t, err)
}

// A session minted before claims were typed (jwt.MapClaims, sv as a JSON
// number) must keep validating: the claim set is the wire contract.
func TestSessionClaimsStayCompatibleWithMapClaimsTokens(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	authService := NewDashboardAuthService(newFakeUserRepo(), newFakeRefreshTokenRepo())

	legacy, err := crypto.GenerateJWTToken("test-secret", jwt.MapClaims{
		"sub":     dashboardSubject,
		"exp":     time.Now().Add(time.Hour).Unix(),
		"iat":     time.Now().Unix(),
		"type":    "token",
		"userId":  "user-1",
		"email":   "member@example.com",
		"isAdmin": true,
		"sv":      3,
	})
	require.NoError(t, err)

	principal, err := authService.ValidateSession(legacy)
	require.NoError(t, err)
	assert.Equal(t, "user-1", principal.UserId)
	assert.Equal(t, "member@example.com", principal.Email)
	assert.True(t, principal.IsAdmin)
	assert.Equal(t, int32(3), principal.SessionVersion)
}
