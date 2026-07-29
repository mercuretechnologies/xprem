// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"errors"
	"testing"

	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"

	"github.com/stretchr/testify/require"
)

type fakeRepo struct {
	roles map[string]Role
	// grants is keyed by userID; the slice order mirrors what the store
	// would return.
	grants map[string][]AppGrant
	// replaced records the last ReplaceUserGrants call for assertions.
	replaced map[string][]GrantInput
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		roles:    map[string]Role{},
		grants:   map[string][]AppGrant{},
		replaced: map[string][]GrantInput{},
	}
}

func (f *fakeRepo) ListRoles(_ context.Context) ([]Role, error) {
	roles := make([]Role, 0, len(f.roles))
	for _, role := range f.roles {
		roles = append(roles, role)
	}
	return roles, nil
}

func (f *fakeRepo) GetRoleByID(_ context.Context, id string) (Role, error) {
	role, ok := f.roles[id]
	if !ok {
		return Role{}, ErrRoleNotFound
	}
	return role, nil
}

func (f *fakeRepo) InsertRole(_ context.Context, role Role) (Role, error) {
	f.roles[role.ID] = role
	return role, nil
}

func (f *fakeRepo) UpdateRole(_ context.Context, id string, name string, permissions []Permission) error {
	role, ok := f.roles[id]
	if !ok {
		return ErrRoleNotFound
	}
	role.Name = name
	role.Permissions = permissions
	f.roles[id] = role
	return nil
}

func (f *fakeRepo) DeleteRole(_ context.Context, id string) error {
	if _, ok := f.roles[id]; !ok {
		return ErrRoleNotFound
	}
	delete(f.roles, id)
	return nil
}

func (f *fakeRepo) ListUserGrants(_ context.Context, userID string) ([]AppGrant, error) {
	return f.grants[userID], nil
}

func (f *fakeRepo) GetUserAppGrant(_ context.Context, userID string, appID string) (*AppGrant, error) {
	for _, grant := range f.grants[userID] {
		if grant.AppID == appID {
			return &grant, nil
		}
	}
	return nil, nil
}

func (f *fakeRepo) ReplaceUserGrants(_ context.Context, userID string, grants []GrantInput) error {
	f.replaced[userID] = grants
	return nil
}

func (f *fakeRepo) GrantCountsByUser(_ context.Context) (map[string]int64, error) {
	counts := map[string]int64{}
	for userID, grants := range f.grants {
		if len(grants) > 0 {
			counts[userID] = int64(len(grants))
		}
	}
	return counts, nil
}

func (f *fakeRepo) ListAccessibleAppIDs(_ context.Context, userID string) ([]string, error) {
	ids := make([]string, 0, len(f.grants[userID]))
	for _, grant := range f.grants[userID] {
		ids = append(ids, grant.AppID)
	}
	return ids, nil
}

func licensedService(repo RBACRepository) *RBACService {
	service := NewRBACService(repo, nil)
	service.licenseValid = func() bool { return true }
	return service
}

func unlicensedService(repo RBACRepository) *RBACService {
	service := NewRBACService(repo, nil)
	service.licenseValid = func() bool { return false }
	return service
}

func withLookup(service *RBACService, lookup UserLookup) *RBACService {
	service.userLookup = lookup
	return service
}

func TestEnabledRequiresRepoAndLicense(t *testing.T) {
	require.False(t, licensedService(nil).Enabled(), "stateless mode can never enforce roles")
	require.False(t, unlicensedService(newFakeRepo()).Enabled(), "no license, community rules")
	require.True(t, licensedService(newFakeRepo()).Enabled())
}

func TestManagementRequiresControlPlane(t *testing.T) {
	ctx := context.Background()
	service := licensedService(nil)

	_, err := service.ListRoles(ctx)
	require.ErrorIs(t, err, ErrRequiresControlPlane)
	_, err = service.CreateRole(ctx, "Release manager", []Permission{PermChannelRolloutManage})
	require.ErrorIs(t, err, ErrRequiresControlPlane)
	_, err = service.GetUserGrants(ctx, "user-1")
	require.ErrorIs(t, err, ErrRequiresControlPlane)
	require.ErrorIs(t, service.SetUserGrants(ctx, "user-1", nil), ErrRequiresControlPlane)
}

func TestWritesRequireLicenseButReadsStayOpen(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	repo.roles["role-1"] = Role{ID: "role-1", Name: "Release manager"}
	service := unlicensedService(repo)

	_, err := service.CreateRole(ctx, "Ops", []Permission{PermBranchDelete})
	require.ErrorIs(t, err, ErrRequiresValidLicense)
	require.ErrorIs(t, service.UpdateRole(ctx, "role-1", "Ops", nil), ErrRequiresValidLicense)
	require.ErrorIs(t, service.DeleteRole(ctx, "role-1"), ErrRequiresValidLicense)
	require.ErrorIs(t, service.SetUserGrants(ctx, "user-1", nil), ErrRequiresValidLicense)

	// Reads keep working so the dashboard can show what exists (dormant).
	roles, err := service.ListRoles(ctx)
	require.NoError(t, err)
	require.Len(t, roles, 1)
	_, err = service.GetUserGrants(ctx, "user-1")
	require.NoError(t, err)
}

func TestCreateRoleValidatesInput(t *testing.T) {
	ctx := context.Background()
	service := licensedService(newFakeRepo())

	validationErr := (*ValidationError)(nil)
	_, err := service.CreateRole(ctx, "   ", []Permission{PermAppDelete})
	require.True(t, errors.As(err, &validationErr), "empty name must be refused, got %v", err)
	_, err = service.CreateRole(ctx, "Ops", []Permission{"branch:invalid"})
	require.True(t, errors.As(err, &validationErr), "unknown permission must be refused, got %v", err)

	role, err := service.CreateRole(ctx, "  Release manager  ", []Permission{PermChannelRolloutManage})
	require.NoError(t, err)
	require.Equal(t, "Release manager", role.Name, "name must be trimmed")
	require.NotEmpty(t, role.ID)
}

func TestSetUserGrantsValidatesInput(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	service := licensedService(repo)

	validationErr := (*ValidationError)(nil)
	err := service.SetUserGrants(ctx, "user-1", []GrantInput{
		{AppID: "app-1"},
		{AppID: "app-1"},
	})
	require.True(t, errors.As(err, &validationErr), "duplicate app must be refused, got %v", err)

	err = service.SetUserGrants(ctx, "user-1", []GrantInput{
		{AppID: "app-1", ExtraPermissions: []Permission{"nope"}},
	})
	require.True(t, errors.As(err, &validationErr), "unknown permission must be refused, got %v", err)

	grants := []GrantInput{{AppID: "app-1", ExtraPermissions: []Permission{PermBranchCreate}}}
	require.NoError(t, service.SetUserGrants(ctx, "user-1", grants))
	require.Equal(t, grants, repo.replaced["user-1"])
}

func TestAuthorize(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	roleID := "role-1"
	repo.grants["user-1"] = []AppGrant{{
		AppID:            "app-1",
		RoleID:           &roleID,
		RolePermissions:  []Permission{PermChannelRolloutManage},
		ExtraPermissions: []Permission{PermBranchCreate},
	}}
	service := licensedService(repo)
	member := Subject{UserID: "user-1"}

	// Through the role and through the direct grant.
	require.NoError(t, service.Authorize(ctx, member, "app-1", PermChannelRolloutManage, FallbackAdminOnly))
	require.NoError(t, service.Authorize(ctx, member, "app-1", PermBranchCreate, FallbackAdminOnly))

	// Granted app, missing permission: a 403 naming the permission.
	err := service.Authorize(ctx, member, "app-1", PermAppDelete, FallbackAdminOnly)
	deniedErr := (*ErrPermissionDenied)(nil)
	require.True(t, errors.As(err, &deniedErr), "expected ErrPermissionDenied, got %v", err)
	require.Equal(t, PermAppDelete, deniedErr.Permission)

	// No grant on the app: it does not exist for this member.
	require.ErrorIs(t, service.Authorize(ctx, member, "app-2", PermBranchCreate, FallbackAdminOnly), ErrNoAppAccess)
	require.ErrorIs(t, service.Authorize(ctx, Subject{UserID: "user-2"}, "app-1", PermBranchCreate, FallbackAdminOnly), ErrNoAppAccess)
}

func TestAuthorizeAdminBypassesEverything(t *testing.T) {
	ctx := context.Background()
	admin := Subject{UserID: "admin-1", IsAdmin: true}

	// No grant row, no license, not even a control plane: an admin is always
	// allowed and always sees everything.
	for _, service := range []*RBACService{licensedService(newFakeRepo()), unlicensedService(newFakeRepo()), unlicensedService(nil)} {
		require.NoError(t, service.Authorize(ctx, admin, "any-app", PermAppDelete, FallbackAdminOnly))
		visible, err := service.CanSeeApp(ctx, admin, "any-app")
		require.NoError(t, err)
		require.True(t, visible)
		restricted, _, err := service.VisibleApps(ctx, admin)
		require.NoError(t, err)
		require.False(t, restricted)
	}
}

func TestAuthorizeFallsBackWithoutLicense(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	repo.grants["user-1"] = []AppGrant{{AppID: "app-1", ExtraPermissions: []Permission{PermAppDelete}}}

	err := unlicensedService(repo).Authorize(ctx, Subject{UserID: "user-1"}, "app-1", PermAppDelete, FallbackAdminOnly)
	require.ErrorIs(t, err, ErrRequiresValidLicense,
		"an expired license must never widen member access beyond community rules")
}

// Without a license, a route that chose FallbackAnyMember keeps letting
// members in.
func TestAuthorizeHonorsAnyMemberFallbackWithoutLicense(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()

	// No grant at all, which is the point: the fallback decides, not the data.
	err := unlicensedService(repo).Authorize(ctx, Subject{UserID: "user-1"}, "app-1", PermObserveRead, FallbackAnyMember)
	require.NoError(t, err)
}

// Once roles are enforced, the fallback no longer applies; only the
// unlicensed case honors it.
func TestAnyMemberFallbackStopsApplyingOnceRolesAreEnforced(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	repo.grants["user-1"] = []AppGrant{{AppID: "app-1", ExtraPermissions: []Permission{PermIdentityRead}}}

	service := licensedService(repo)
	require.NoError(t, service.Authorize(ctx, Subject{UserID: "user-1"}, "app-1", PermIdentityRead, FallbackAnyMember))

	err := service.Authorize(ctx, Subject{UserID: "user-1"}, "app-1", PermObserveRead, FallbackAnyMember)
	var denied *ErrPermissionDenied
	require.ErrorAs(t, err, &denied, "a granted app without the permission must still refuse")
}

// A non-admin subject should never reach the no-control-plane branch; it
// refuses regardless of fallback.
func TestAuthorizeRefusesWithoutControlPlaneEvenOnAnyMemberFallback(t *testing.T) {
	ctx := context.Background()
	service := NewRBACService(nil, nil)
	service.licenseValid = func() bool { return true }

	err := service.Authorize(ctx, Subject{UserID: "user-1"}, "app-1", PermObserveRead, FallbackAnyMember)
	require.ErrorIs(t, err, ErrRequiresControlPlane)
}

func TestMemberVisibility(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	repo.grants["user-1"] = []AppGrant{{AppID: "app-1"}, {AppID: "app-2"}}
	member := Subject{UserID: "user-1"}
	strangerMember := Subject{UserID: "user-2"}

	// Community fallback: nothing is restricted.
	restricted, _, err := unlicensedService(repo).VisibleApps(ctx, member)
	require.NoError(t, err)
	require.False(t, restricted)
	visible, err := unlicensedService(repo).CanSeeApp(ctx, member, "app-3")
	require.NoError(t, err)
	require.True(t, visible)

	// Enforced: only granted apps, and a grant-less member sees nothing.
	service := licensedService(repo)
	restricted, ids, err := service.VisibleApps(ctx, member)
	require.NoError(t, err)
	require.True(t, restricted)
	require.ElementsMatch(t, []string{"app-1", "app-2"}, ids)

	restricted, ids, err = service.VisibleApps(ctx, strangerMember)
	require.NoError(t, err)
	require.True(t, restricted)
	require.Empty(t, ids)

	visible, err = service.CanSeeApp(ctx, member, "app-2")
	require.NoError(t, err)
	require.True(t, visible)
	visible, err = service.CanSeeApp(ctx, member, "app-3")
	require.NoError(t, err)
	require.False(t, visible)
}

func TestVisibleAppsForPrincipal(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	repo.grants["member-1"] = []AppGrant{{AppID: "app-1"}}
	lookup := &fakeUserLookup{users: map[string]store.User{
		"member-1": {Id: "member-1"},
		"admin-1":  {Id: "admin-1", IsAdmin: true},
	}}
	service := withLookup(licensedService(repo), lookup)

	// No principal (CLI) and community fallback: unrestricted.
	restricted, _, err := service.VisibleAppsForPrincipal(ctx, nil)
	require.NoError(t, err)
	require.False(t, restricted)
	restricted, _, err = withLookup(unlicensedService(repo), lookup).VisibleAppsForPrincipal(ctx, &services.DashboardPrincipal{UserId: "member-1"})
	require.NoError(t, err)
	require.False(t, restricted)

	// A member sees their granted set; the admin flag is read fresh, so a
	// stale admin claim does not widen anything.
	restricted, visible, err := service.VisibleAppsForPrincipal(ctx, &services.DashboardPrincipal{UserId: "member-1", IsAdmin: true})
	require.NoError(t, err)
	require.True(t, restricted)
	require.Equal(t, map[string]bool{"app-1": true}, visible)

	// A real admin is unrestricted.
	restricted, _, err = service.VisibleAppsForPrincipal(ctx, &services.DashboardPrincipal{UserId: "admin-1"})
	require.NoError(t, err)
	require.False(t, restricted)

	// A deleted account's leftover session sees an empty world, not an error.
	restricted, visible, err = service.VisibleAppsForPrincipal(ctx, &services.DashboardPrincipal{UserId: "ghost"})
	require.NoError(t, err)
	require.True(t, restricted)
	require.Empty(t, visible)
}

func TestEffectivePermissionsDeduplicateInCatalogOrder(t *testing.T) {
	grant := AppGrant{
		AppID:            "app-1",
		RolePermissions:  []Permission{PermChannelRolloutManage, PermBranchCreate},
		ExtraPermissions: []Permission{PermBranchCreate, PermAppDelete},
	}
	require.Equal(t,
		[]Permission{PermAppDelete, PermBranchCreate, PermChannelRolloutManage},
		grant.Effective())

	ctx := context.Background()
	repo := newFakeRepo()
	repo.grants["user-1"] = []AppGrant{grant, {AppID: "app-2"}}
	byApp, err := licensedService(repo).EffectivePermissionsByApp(ctx, "user-1")
	require.NoError(t, err)
	require.Len(t, byApp, 2)
	require.Equal(t, []Permission{PermAppDelete, PermBranchCreate, PermChannelRolloutManage}, byApp["app-1"])
	require.Empty(t, byApp["app-2"], "a role-less, permission-less grant still lists the app (visibility)")
}
