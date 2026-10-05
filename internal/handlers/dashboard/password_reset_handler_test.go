package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"xprem/internal/crypto"
	"xprem/internal/services"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetUserPassword(t *testing.T, handler *UsersHandler, principal *services.DashboardPrincipal, targetID, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/api/users/"+targetID+"/password", strings.NewReader(body))
	if principal != nil {
		request = request.WithContext(services.WithPrincipal(request.Context(), principal))
	}
	request = mux.SetURLVars(request, map[string]string{"USER_ID": targetID})
	recorder := httptest.NewRecorder()
	handler.ResetUserPasswordHandler(recorder, request)
	return recorder
}

func passwordResetAdmin() *services.DashboardPrincipal {
	return &services.DashboardPrincipal{
		UserId: "22222222-2222-2222-2222-222222222222", Email: "admin@example.com", IsAdmin: true,
	}
}

func TestResetUserPasswordRequiresAnAdminDashboardSession(t *testing.T) {
	fixture := newPasswordFixture(t)
	before := fixture.repo.user
	for _, tc := range []struct {
		name      string
		principal *services.DashboardPrincipal
		status    int
	}{
		{name: "unauthenticated", status: http.StatusUnauthorized},
		{name: "member", principal: fixture.principal, status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := resetUserPassword(t, fixture.handler, tc.principal, fixture.repo.user.Id,
				`{"newPassword":"An0therSecret!"}`)
			assert.Equal(t, tc.status, recorder.Code)
		})
	}
	assert.Equal(t, before, fixture.repo.user)
	assert.Empty(t, fixture.ledger.tokens)
}

func TestResetUserPasswordRejectsCLICredentials(t *testing.T) {
	fixture := newPasswordFixture(t)
	request := httptest.NewRequest(http.MethodPut, "/api/users/"+fixture.repo.user.Id+"/password",
		strings.NewReader(`{"newPassword":"An0therSecret!"}`))
	request = request.WithContext(services.WithCliAuth(context.Background(), services.CliCredential{AppID: "app"}))
	request = mux.SetURLVars(request, map[string]string{"USER_ID": fixture.repo.user.Id})
	recorder := httptest.NewRecorder()

	fixture.handler.ResetUserPasswordHandler(recorder, request)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.EqualValues(t, 0, fixture.repo.user.SessionVersion)
}

func TestResetUserPasswordReturns204WithoutIssuingATargetSession(t *testing.T) {
	fixture := newPasswordFixture(t)
	// An admin reset must not depend on issuing a session for the target,
	// nor issue credentials that let the administrator impersonate it.
	fixture.ledger.insertErr = assert.AnError

	recorder := resetUserPassword(t, fixture.handler, passwordResetAdmin(), fixture.repo.user.Id,
		`{"newPassword":"An0therSecret!"}`)

	require.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Empty(t, recorder.Body.String())
	assert.Empty(t, fixture.ledger.tokens)
	assert.EqualValues(t, 1, fixture.repo.user.SessionVersion)
	assert.True(t, crypto.VerifyPassword(fixture.repo.user.PasswordHash, "An0therSecret!"))
	assert.False(t, crypto.VerifyPassword(fixture.repo.user.PasswordHash, "Sup3rSecret!"))
}

func TestResetUserPasswordRejectsInvalidInputWithoutChangingTheAccount(t *testing.T) {
	fixture := newPasswordFixture(t)
	before := fixture.repo.user
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: "{"},
		{name: "empty body", body: ""},
		{name: "missing password", body: `{}`},
		{name: "empty password", body: `{"newPassword":""}`},
		{name: "wrong type", body: `{"newPassword":42}`},
		{name: "policy failure", body: `{"newPassword":"weak"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := resetUserPassword(t, fixture.handler, passwordResetAdmin(), fixture.repo.user.Id, tc.body)
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
	assert.Equal(t, before, fixture.repo.user)
	assert.Empty(t, fixture.ledger.tokens)
}

func TestResetUserPasswordUnknownTargetReturns404(t *testing.T) {
	fixture := newPasswordFixture(t)
	before := fixture.repo.user

	recorder := resetUserPassword(t, fixture.handler, passwordResetAdmin(), "unknown",
		`{"newPassword":"An0therSecret!"}`)

	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, before, fixture.repo.user)
}

func TestResetUserPasswordOwnAccountRequiresCurrentPassword(t *testing.T) {
	fixture := newPasswordFixture(t)
	before := fixture.repo.user
	principal := *fixture.principal
	principal.IsAdmin = true

	recorder := resetUserPassword(t, fixture.handler, &principal, principal.UserId,
		`{"newPassword":"An0therSecret!"}`)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Equal(t, before, fixture.repo.user)
}

func TestResetUserPasswordRequiresTheControlPlane(t *testing.T) {
	fixture := newPasswordFixture(t)
	fixture.handler.userService = services.NewUserService(nil)

	recorder := resetUserPassword(t, fixture.handler, passwordResetAdmin(), fixture.repo.user.Id,
		`{"newPassword":"An0therSecret!"}`)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.EqualValues(t, 0, fixture.repo.user.SessionVersion)
}

func TestResetUserPasswordSSOMemberReturns409(t *testing.T) {
	fixture := newPasswordFixture(t)
	before := fixture.repo.user
	fixture.handler.userService.SetSSOEnforced(func(context.Context) bool { return true })

	recorder := resetUserPassword(t, fixture.handler, passwordResetAdmin(), fixture.repo.user.Id,
		`{"newPassword":"An0therSecret!"}`)

	assert.Equal(t, http.StatusConflict, recorder.Code)
	assert.Equal(t, before, fixture.repo.user)
}
