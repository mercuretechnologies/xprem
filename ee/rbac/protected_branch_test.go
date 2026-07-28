// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"errors"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeBranchProtection answers from a fixed set. An empty set is the shape of
// a deployment where protection is not enforced: apikeyrestrictions answers
// false for every branch without a license.
type fakeBranchProtection struct {
	protected map[string]bool
	err       error
}

func (f *fakeBranchProtection) IsBranchProtected(_ context.Context, _ string, branchName string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.protected[branchName], nil
}

// guardRequest is the request as the publish handler sees it: the route's own
// permission middleware has already passed, so the principal is on the context.
func guardRequest(principal *services.DashboardPrincipal) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/apps/app-1/branch/production/rollback", nil)
	if principal == nil {
		return req
	}
	return req.WithContext(services.WithPrincipal(req.Context(), principal))
}

func memberService(t *testing.T, permissions ...Permission) *RBACService {
	t.Helper()
	repo := newFakeRepo()
	repo.grants["member-1"] = []AppGrant{{AppID: "app-1", ExtraPermissions: permissions}}
	lookup := &fakeUserLookup{users: map[string]store.User{"member-1": {Id: "member-1"}}}
	return withLookup(licensedService(repo), lookup)
}

func TestProtectedBranchGuardOnlyBitesOnProtectedBranches(t *testing.T) {
	guard := NewProtectedBranchGuard(
		memberService(t, PermUpdatePublish),
		&fakeBranchProtection{protected: map[string]bool{"production": true}},
		PermUpdatePublishProtected,
	)
	request := guardRequest(&services.DashboardPrincipal{UserId: "member-1"})

	// update:publish alone is the whole answer on an unprotected branch.
	require.NoError(t, guard(request, "app-1", "staging"))

	// The same grant is refused on the protected one, and the message names
	// the branch and the permission so the member knows what to ask for.
	err := guard(request, "app-1", "production")
	require.ErrorIs(t, err, handlers.ErrAccessDenied)
	require.Contains(t, err.Error(), "production")
	require.Contains(t, err.Error(), string(PermUpdatePublishProtected))
}

func TestProtectedBranchGuardPassesWithThePermission(t *testing.T) {
	guard := NewProtectedBranchGuard(
		memberService(t, PermUpdatePublish, PermUpdatePublishProtected),
		&fakeBranchProtection{protected: map[string]bool{"production": true}},
		PermUpdatePublishProtected,
	)
	require.NoError(t, guard(guardRequest(&services.DashboardPrincipal{UserId: "member-1"}), "app-1", "production"))
}

func TestProtectedBranchGuardAdminBypasses(t *testing.T) {
	lookup := &fakeUserLookup{users: map[string]store.User{"admin-1": {Id: "admin-1", IsAdmin: true}}}
	guard := NewProtectedBranchGuard(
		withLookup(licensedService(newFakeRepo()), lookup),
		&fakeBranchProtection{protected: map[string]bool{"production": true}},
		PermUpdatePublishProtected,
	)
	require.NoError(t, guard(guardRequest(&services.DashboardPrincipal{UserId: "admin-1"}), "app-1", "production"))
}

// Unplugged (community) the guard is not reachable at all, but a half-wired
// one must not fail closed on every branch either.
func TestProtectedBranchGuardWithoutProtectionWiring(t *testing.T) {
	guard := NewProtectedBranchGuard(memberService(t, PermUpdatePublish), nil, PermUpdatePublishProtected)
	require.NoError(t, guard(guardRequest(&services.DashboardPrincipal{UserId: "member-1"}), "app-1", "production"))
}

// An unreadable protection flag must refuse the publish, and as a 500 rather
// than a denial: the branch it could not classify may be the protected one,
// and telling the member to ask for a permission would be a lie.
func TestProtectedBranchGuardFailsClosedOnReadError(t *testing.T) {
	guard := NewProtectedBranchGuard(
		memberService(t, PermUpdatePublish),
		&fakeBranchProtection{err: errors.New("database is down")},
		PermUpdatePublishProtected,
	)
	err := guard(guardRequest(&services.DashboardPrincipal{UserId: "member-1"}), "app-1", "production")
	require.Error(t, err)
	require.NotErrorIs(t, err, handlers.ErrAccessDenied)
}

// A request that reached the handler without a principal cannot be judged, so
// a protected branch refuses it rather than assuming the route vouched for it.
func TestProtectedBranchGuardRefusesWithoutPrincipal(t *testing.T) {
	guard := NewProtectedBranchGuard(
		memberService(t, PermUpdatePublish),
		&fakeBranchProtection{protected: map[string]bool{"production": true}},
		PermUpdatePublishProtected,
	)
	require.ErrorIs(t, guard(guardRequest(nil), "app-1", "production"), handlers.ErrAccessDenied)
}
