// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"xprem/internal/services"
	"xprem/internal/store"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

// newHandlerRig registers every RBAC route the way the real router does
// (minus the admin gate, which has its own tests) and returns a request
// runner. The principal, when set, rides the context like the auth middleware
// would place it.
func newHandlerRig(handler *RBACHandler) func(t *testing.T, method, path, body string, principal *services.DashboardPrincipal) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	router.HandleFunc("/roles", handler.ListRolesHandler).Methods(http.MethodGet)
	router.HandleFunc("/roles", handler.CreateRoleHandler).Methods(http.MethodPost)
	router.HandleFunc("/roles/{ROLE_ID}", handler.UpdateRoleHandler).Methods(http.MethodPut)
	router.HandleFunc("/roles/{ROLE_ID}", handler.DeleteRoleHandler).Methods(http.MethodDelete)
	router.HandleFunc("/users/{USER_ID}/grants", handler.GetUserGrantsHandler).Methods(http.MethodGet)
	router.HandleFunc("/users/{USER_ID}/grants", handler.SetUserGrantsHandler).Methods(http.MethodPut)
	router.HandleFunc("/me/permissions", handler.GetMyPermissionsHandler).Methods(http.MethodGet)
	return func(t *testing.T, method, path, body string, principal *services.DashboardPrincipal) *httptest.ResponseRecorder {
		t.Helper()
		ctx := context.Background()
		if principal != nil {
			ctx = services.WithPrincipal(ctx, principal)
		}
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}
}

func TestRoleHandlersContract(t *testing.T) {
	repo := newFakeRepo()
	run := newHandlerRig(NewRBACHandler(withLookup(licensedService(repo), &fakeUserLookup{})))

	// Create: 201 with the canonical shape.
	recorder := run(t, http.MethodPost, "/roles", `{"name":"Release manager","permissions":["channel-rollout:manage"]}`, nil)
	require.Equal(t, http.StatusCreated, recorder.Code)
	var created RoleResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))
	require.NotEmpty(t, created.Id)
	require.Equal(t, "Release manager", created.Name)
	require.Equal(t, []string{"channel-rollout:manage"}, created.Permissions)

	// An unknown permission string never reaches the repository.
	recorder = run(t, http.MethodPost, "/roles", `{"name":"Bad","permissions":["nope"]}`, nil)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "nope")

	// List carries the created role.
	recorder = run(t, http.MethodGet, "/roles", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	var listed []RoleResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &listed))
	require.Len(t, listed, 1)

	// Update and delete answer 204; a ghost role 404s.
	recorder = run(t, http.MethodPut, "/roles/"+created.Id, `{"name":"Ops","permissions":[]}`, nil)
	require.Equal(t, http.StatusNoContent, recorder.Code)
	recorder = run(t, http.MethodDelete, "/roles/"+created.Id, "", nil)
	require.Equal(t, http.StatusNoContent, recorder.Code)
	recorder = run(t, http.MethodDelete, "/roles/"+created.Id, "", nil)
	require.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestGrantHandlersContract(t *testing.T) {
	repo := newFakeRepo()
	roleID := "role-1"
	repo.grants["member-1"] = []AppGrant{{
		AppID:            "app-1",
		RoleID:           &roleID,
		RolePermissions:  []Permission{PermBranchDelete},
		ExtraPermissions: []Permission{PermCertificateRead},
	}}
	lookup := &fakeUserLookup{users: map[string]store.User{"member-1": {Id: "member-1"}}}
	run := newHandlerRig(NewRBACHandler(withLookup(licensedService(repo), lookup)))

	// The read resolves the role and precomputes the effective union.
	recorder := run(t, http.MethodGet, "/users/member-1/grants", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	var grants []GrantResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &grants))
	require.Len(t, grants, 1)
	require.Equal(t, "app-1", grants[0].AppId)
	require.Equal(t, []string{"certificate:read"}, grants[0].ExtraPermissions)
	require.Equal(t, []string{"certificate:read", "branch:delete"}, grants[0].EffectivePermissions)

	// Writes land in the repository as a wholesale replacement.
	recorder = run(t, http.MethodPut, "/users/member-1/grants",
		`[{"appId":"app-2","roleId":"role-9","extraPermissions":["branch:create"]}]`, nil)
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Len(t, repo.replaced["member-1"], 1)
	require.Equal(t, "app-2", repo.replaced["member-1"][0].AppID)

	// A grants request for an account that does not exist is a 404, not an
	// empty list.
	recorder = run(t, http.MethodGet, "/users/ghost/grants", "", nil)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	recorder = run(t, http.MethodPut, "/users/ghost/grants", `[]`, nil)
	require.Equal(t, http.StatusNotFound, recorder.Code)

	// Unknown permission strings are refused at the door.
	recorder = run(t, http.MethodPut, "/users/member-1/grants", `[{"appId":"app-1","extraPermissions":["nope"]}]`, nil)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestMyPermissionsHandler(t *testing.T) {
	repo := newFakeRepo()
	repo.grants["member-1"] = []AppGrant{{AppID: "app-1", ExtraPermissions: []Permission{PermBranchCreate}}}
	lookup := &fakeUserLookup{users: map[string]store.User{
		"member-1": {Id: "member-1"},
		"admin-1":  {Id: "admin-1", IsAdmin: true},
	}}

	decode := func(recorder *httptest.ResponseRecorder) MyPermissionsResponse {
		var response MyPermissionsResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		return response
	}

	// Enforced member: the per-app map, straight from the grants.
	run := newHandlerRig(NewRBACHandler(withLookup(licensedService(repo), lookup)))
	recorder := run(t, http.MethodGet, "/me/permissions", "", &services.DashboardPrincipal{UserId: "member-1"})
	require.Equal(t, http.StatusOK, recorder.Code)
	response := decode(recorder)
	require.True(t, response.RBACEnabled)
	require.False(t, response.IsAdmin)
	require.Equal(t, map[string][]string{"app-1": {"branch:create"}}, response.Apps)

	// Admin: flagged as such, no map needed.
	recorder = run(t, http.MethodGet, "/me/permissions", "", &services.DashboardPrincipal{UserId: "admin-1"})
	response = decode(recorder)
	require.True(t, response.IsAdmin)
	require.Nil(t, response.Apps)

	// No license: enabled=false tells the UI to fall back to community rules.
	run = newHandlerRig(NewRBACHandler(withLookup(unlicensedService(repo), lookup)))
	recorder = run(t, http.MethodGet, "/me/permissions", "", &services.DashboardPrincipal{UserId: "member-1"})
	response = decode(recorder)
	require.False(t, response.RBACEnabled)
	require.Nil(t, response.Apps)

	// No session at all: refused.
	recorder = run(t, http.MethodGet, "/me/permissions", "", nil)
	require.Equal(t, http.StatusForbidden, recorder.Code)
}
