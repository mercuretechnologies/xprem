package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cache2 "xprem/internal/cache"
	"xprem/internal/crypto"
	"xprem/internal/ratelimit"
	"xprem/internal/services"
	"xprem/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Changing a password revokes every session of the account, the caller's
// included, so this endpoint hands back a replacement pair. Both halves of the
// change depend on that contract: the dashboard reads the pair and stays
// signed in, and falls back to the login page when the body is empty. Nothing
// tested it on either side, so a silent regression to 204 would sign every
// account out on a password change with only a log line to say so.

// fakePasswordUserRepo is the users table, reduced to what this endpoint
// touches. The rest panics rather than returning zero values, so a future call
// path shows up as a failure instead of as a plausible-looking answer.
type fakePasswordUserRepo struct {
	user store.User
}

func (r *fakePasswordUserRepo) GetUserByID(_ context.Context, id string) (store.User, error) {
	if id != r.user.Id {
		return store.User{}, &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	return r.user, nil
}

// Mirrors the SQL: the new password and the revocation land in one statement.
func (r *fakePasswordUserRepo) UpdateUserPassword(_ context.Context, _ string, passwordHash string) error {
	r.user.PasswordHash = passwordHash
	r.user.SessionVersion++
	return nil
}

func (r *fakePasswordUserRepo) BumpUserSessionVersion(_ context.Context, _ string) error {
	r.user.SessionVersion++
	return nil
}

func (r *fakePasswordUserRepo) TouchUserLastConnected(_ context.Context, _ string) error { return nil }

func (r *fakePasswordUserRepo) InsertUser(context.Context, store.InsertUserParameters) (store.User, error) {
	panic("unused")
}
func (r *fakePasswordUserRepo) GetUserByEmail(context.Context, string) (store.User, error) {
	panic("unused")
}
func (r *fakePasswordUserRepo) GetUsers(context.Context) ([]store.User, error) { panic("unused") }
func (r *fakePasswordUserRepo) DeleteUserByID(context.Context, string) error   { panic("unused") }
func (r *fakePasswordUserRepo) UpdateUserIsAdmin(context.Context, string, bool) error {
	panic("unused")
}
func (r *fakePasswordUserRepo) UpdateUserEnabled(context.Context, string, bool) error {
	panic("unused")
}

// fakeLedger is the rotation ledger, in memory. The replacement session needs
// a row like any other sign-in.
type fakeLedger struct {
	tokens    map[string]store.RefreshToken
	insertErr error
}

func (l *fakeLedger) InsertRefreshToken(_ context.Context, params store.InsertRefreshTokenParameters) error {
	if l.insertErr != nil {
		return l.insertErr
	}
	l.tokens[params.ID] = store.RefreshToken{Id: params.ID, UserId: params.UserID, FamilyId: params.FamilyID, ExpiresAt: params.ExpiresAt}
	return nil
}
func (l *fakeLedger) DeleteExpiredRefreshTokens(context.Context, string) error { return nil }
func (l *fakeLedger) RotateRefreshToken(context.Context, store.RotateRefreshTokenParameters) (store.RefreshToken, error) {
	panic("unused")
}
func (l *fakeLedger) GetRefreshToken(context.Context, string, time.Duration) (store.RefreshToken, error) {
	panic("unused")
}
func (l *fakeLedger) DeleteRefreshTokenFamily(context.Context, string) error { panic("unused") }

func changePassword(t *testing.T, handler *UsersHandler, principal *services.DashboardPrincipal, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/api/me/password", strings.NewReader(body))
	request = request.WithContext(services.WithPrincipal(context.Background(), principal))
	recorder := httptest.NewRecorder()
	handler.ChangeMyPasswordHandler(recorder, request)
	return recorder
}

type passwordFixture struct {
	handler   *UsersHandler
	repo      *fakePasswordUserRepo
	ledger    *fakeLedger
	auth      *services.DashboardAuthService
	principal *services.DashboardPrincipal
}

func newPasswordFixture(t *testing.T) *passwordFixture {
	t.Helper()
	t.Setenv("JWT_SECRET", "test-secret")
	hash, err := crypto.HashPassword("Sup3rSecret!")
	require.NoError(t, err)
	repo := &fakePasswordUserRepo{user: store.User{
		Id: "11111111-1111-1111-1111-111111111111", Email: "member@example.com", PasswordHash: hash, Enabled: true,
	}}
	ledger := &fakeLedger{tokens: map[string]store.RefreshToken{}}
	authService := services.NewDashboardAuthService(repo, ledger)
	return &passwordFixture{
		handler:   NewUsersHandler(services.NewUserService(repo), authService, ratelimit.New(cache2.GetCache())),
		repo:      repo,
		ledger:    ledger,
		auth:      authService,
		principal: &services.DashboardPrincipal{UserId: repo.user.Id, Email: repo.user.Email},
	}
}

func TestChangePasswordReturnsAWorkingReplacementSession(t *testing.T) {
	fixture := newPasswordFixture(t)

	recorder := changePassword(t, fixture.handler, fixture.principal,
		`{"currentPassword":"Sup3rSecret!","newPassword":"An0therSecret!"}`)

	require.Equal(t, http.StatusOK, recorder.Code)
	var session services.DashboardSession
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &session))
	require.NotEmpty(t, session.Token)
	require.NotEmpty(t, session.RefreshToken)

	// The replacement carries the NEW generation, so it survives the very
	// revocation the password change just performed. Handing back a pair
	// minted before the bump would be worse than handing back nothing.
	_, err := fixture.auth.AuthenticateSession(context.Background(), session.Token)
	assert.NoError(t, err)
	assert.EqualValues(t, 1, fixture.repo.user.SessionVersion)
}

// The 204 the client's fallback branch exists for: the password changed and
// the sessions were revoked, only the replacement could not be minted. Saying
// "error" here would tell the client the opposite of what happened.
func TestChangePasswordAnswers204WhenNoReplacementCanBeIssued(t *testing.T) {
	fixture := newPasswordFixture(t)
	fixture.ledger.insertErr = assert.AnError

	recorder := changePassword(t, fixture.handler, fixture.principal,
		`{"currentPassword":"Sup3rSecret!","newPassword":"An0therSecret!"}`)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Empty(t, recorder.Body.String())
	assert.EqualValues(t, 1, fixture.repo.user.SessionVersion, "the revocation still happened")
}

func TestChangePasswordRejectsAWrongCurrentPassword(t *testing.T) {
	fixture := newPasswordFixture(t)
	before := fixture.repo.user.PasswordHash

	recorder := changePassword(t, fixture.handler, fixture.principal,
		`{"currentPassword":"wrong","newPassword":"An0therSecret!"}`)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Equal(t, before, fixture.repo.user.PasswordHash, "the stored password must not have moved")
	assert.EqualValues(t, 0, fixture.repo.user.SessionVersion, "and no session may have been revoked")
}
