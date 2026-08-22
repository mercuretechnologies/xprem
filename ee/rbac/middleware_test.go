// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"xprem/internal/services"
	"xprem/internal/store"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

type fakeUserLookup struct {
	users map[string]store.User
}

func (f *fakeUserLookup) GetUserByID(_ context.Context, id string) (store.User, error) {
	user, ok := f.users[id]
	if !ok {
		return store.User{}, &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	return user, nil
}

// performAppRequest sends a request through the middleware on an app-scoped
// route; a 200 means the middleware let it through.
func performAppRequest(t *testing.T, mw mux.MiddlewareFunc, principal *services.DashboardPrincipal) *httptest.ResponseRecorder {
	t.Helper()
	router := mux.NewRouter()
	router.Handle("/api/apps/{APP_ID}/branches",
		mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("handler executed"))
		}))).Methods(http.MethodPost)
	req := httptest.NewRequest(http.MethodPost, "/api/apps/app-1/branches", nil)
	if principal != nil {
		req = req.WithContext(services.WithPrincipal(req.Context(), principal))
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

// performCliAppRequest simulates a request the auth middleware validated as
// an app-scoped CLI credential: no principal, the CLI marker on the context.
func performCliAppRequest(t *testing.T, mw mux.MiddlewareFunc) *httptest.ResponseRecorder {
	t.Helper()
	router := mux.NewRouter()
	router.Handle("/api/apps/{APP_ID}/branches",
		mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("handler executed"))
		}))).Methods(http.MethodPost)
	req := httptest.NewRequest(http.MethodPost, "/api/apps/app-1/branches", nil)
	req = req.WithContext(services.WithCliAuth(req.Context(), services.CliCredential{AppID: "app-1"}))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestRequirePermissionRequiresDashboardSession(t *testing.T) {
	mw := RequirePermission(licensedService(newFakeRepo()), PermBranchCreate, FallbackAdminOnly)
	recorder := performAppRequest(t, mw, nil)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "dashboard session")
}

func TestRequirePermissionStatelessTrustsClaim(t *testing.T) {
	// No user lookup (stateless mode): the claim decides, and members are
	// read-only exactly like today.
	service := unlicensedService(nil)
	admin := &services.DashboardPrincipal{UserId: "admin-1", IsAdmin: true}
	member := &services.DashboardPrincipal{UserId: "member-1", IsAdmin: false}

	require.Equal(t, http.StatusOK, performAppRequest(t, RequirePermission(service, PermBranchCreate, FallbackAdminOnly), admin).Code)
	recorder := performAppRequest(t, RequirePermission(service, PermBranchCreate, FallbackAdminOnly), member)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "requires an admin account")
}

func TestRequirePermissionCommunityFallbackWithoutLicense(t *testing.T) {
	// Control plane, grants in place, but no license: the member gets the
	// community admin-only refusal.
	repo := newFakeRepo()
	repo.grants["member-1"] = []AppGrant{{AppID: "app-1", ExtraPermissions: []Permission{PermBranchCreate}}}
	lookup := &fakeUserLookup{users: map[string]store.User{"member-1": {Id: "member-1"}}}
	mw := RequirePermission(withLookup(unlicensedService(repo), lookup), PermBranchCreate, FallbackAdminOnly)

	recorder := performAppRequest(t, mw, &services.DashboardPrincipal{UserId: "member-1"})
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "requires an admin account")
}

func TestRequirePermissionEnforcedMember(t *testing.T) {
	repo := newFakeRepo()
	repo.grants["member-1"] = []AppGrant{{AppID: "app-1", ExtraPermissions: []Permission{PermBranchCreate}}}
	lookup := &fakeUserLookup{users: map[string]store.User{"member-1": {Id: "member-1"}}}
	service := withLookup(licensedService(repo), lookup)
	member := &services.DashboardPrincipal{UserId: "member-1"}

	// Granted permission passes.
	require.Equal(t, http.StatusOK,
		performAppRequest(t, RequirePermission(service, PermBranchCreate, FallbackAdminOnly), member).Code)

	// Granted app, missing permission: 403 naming it.
	recorder := performAppRequest(t, RequirePermission(service, PermBranchDelete, FallbackAdminOnly), member)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), string(PermBranchDelete))

	// No grant on the app at all: it does not exist for this member.
	repo.grants["member-1"] = nil
	recorder = performAppRequest(t, RequirePermission(service, PermBranchCreate, FallbackAdminOnly), member)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Contains(t, recorder.Body.String(), "app not found")
}

func TestRequirePermissionTrustsPrincipalAdminFlag(t *testing.T) {
	// The auth layer reads IsAdmin from the users table on every request, so
	// the middleware judges the principal as is: no second read, and the
	// lookup is never consulted.
	lookup := &fakeUserLookup{users: map[string]store.User{"user-1": {Id: "user-1", IsAdmin: false}}}
	service := withLookup(licensedService(newFakeRepo()), lookup)
	require.Equal(t, http.StatusOK,
		performAppRequest(t, RequirePermission(service, PermBranchCreate, FallbackAdminOnly),
			&services.DashboardPrincipal{UserId: "user-1", IsAdmin: true}).Code)
	require.Equal(t, http.StatusNotFound,
		performAppRequest(t, RequirePermission(service, PermBranchCreate, FallbackAdminOnly),
			&services.DashboardPrincipal{UserId: "user-1", IsAdmin: false}).Code)
}

// countingRepo counts grant reads so a test can assert the middlewares share one.
type countingRepo struct {
	RBACRepository
	grantReads int
}

func (c *countingRepo) GetUserAppGrant(ctx context.Context, userID string, appID string) (*AppGrant, error) {
	c.grantReads++
	return c.RBACRepository.GetUserAppGrant(ctx, userID, appID)
}

func TestRequirePermissionReusesGrantLoadedByRequireAppVisible(t *testing.T) {
	inner := newFakeRepo()
	inner.grants["member-1"] = []AppGrant{{AppID: "app-1", ExtraPermissions: []Permission{PermBranchCreate}}}
	repo := &countingRepo{RBACRepository: inner}
	service := licensedService(repo)
	member := &services.DashboardPrincipal{UserId: "member-1"}

	chain := func(next http.Handler) http.Handler {
		return RequireAppVisible(service)(RequirePermission(service, PermBranchCreate, FallbackAdminOnly)(next))
	}
	require.Equal(t, http.StatusOK, performAppRequest(t, chain, member).Code)
	require.Equal(t, 1, repo.grantReads)

	// The shared grant still decides: a missing permission is a 403.
	repo.grantReads = 0
	denied := func(next http.Handler) http.Handler {
		return RequireAppVisible(service)(RequirePermission(service, PermBranchDelete, FallbackAdminOnly)(next))
	}
	recorder := performAppRequest(t, denied, member)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), string(PermBranchDelete))
	require.Equal(t, 1, repo.grantReads)

	// RequirePermission mounted alone still reads the grant itself.
	repo.grantReads = 0
	require.Equal(t, http.StatusOK,
		performAppRequest(t, RequirePermission(service, PermBranchCreate, FallbackAdminOnly), member).Code)
	require.Equal(t, 1, repo.grantReads)
}

func TestRequireAppVisible(t *testing.T) {
	repo := newFakeRepo()
	repo.grants["member-1"] = []AppGrant{{AppID: "app-1"}}
	lookup := &fakeUserLookup{users: map[string]store.User{
		"member-1": {Id: "member-1"},
		"admin-1":  {Id: "admin-1", IsAdmin: true},
	}}

	enforced := RequireAppVisible(withLookup(licensedService(repo), lookup))

	// A validated CLI credential passes on the auth middleware's explicit
	// marker; a request with neither principal nor marker fails closed.
	require.Equal(t, http.StatusOK, performCliAppRequest(t, enforced).Code)
	require.Equal(t, http.StatusForbidden, performAppRequest(t, enforced, nil).Code)

	// A granted member sees the app, an admin always does.
	member := &services.DashboardPrincipal{UserId: "member-1"}
	require.Equal(t, http.StatusOK, performAppRequest(t, enforced, member).Code)
	require.Equal(t, http.StatusOK,
		performAppRequest(t, enforced, &services.DashboardPrincipal{UserId: "admin-1", IsAdmin: true}).Code)

	// A grant on another app does not help: this one does not exist for them.
	repo.grants["member-1"] = []AppGrant{{AppID: "app-2"}}
	recorder := performAppRequest(t, enforced, member)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Contains(t, recorder.Body.String(), "app not found")

	// Community fallback: everything stays visible.
	require.Equal(t, http.StatusOK,
		performAppRequest(t, RequireAppVisible(withLookup(unlicensedService(repo), lookup)), member).Code)
}
