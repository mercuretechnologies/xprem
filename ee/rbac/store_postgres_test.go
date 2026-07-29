// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Integration tests for the RBAC store: the transactional grant replacement,
// the FK mappings (role in use, unknown app/role) and the cascades need a
// real Postgres. They skip unless TEST_DATABASE_URL is set, e.g.:
//
//	docker run -d --name eoo-pg -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:16-alpine
//	TEST_DATABASE_URL="postgres://postgres:test@localhost:55432/postgres?sslmode=disable" go test ./ee/rbac/

package rbac

import (
	"context"
	"errors"
	"os"
	"testing"

	"xprem/internal/database"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func setupRBACStore(t *testing.T) (*PostgresRBACStore, *pgxpool.Pool) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		// See the same guard in the sso store tests: a skip in CI is a green
		// job that ran none of these guarded queries.
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI: these tests cover SQL that the in-memory fakes cannot reach")
		}
		t.Skip("TEST_DATABASE_URL not set — start a Postgres and set it to run the rbac store tests")
	}
	// The seed migration fails fast on an empty database without the
	// bootstrap pair.
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(dbURL)

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return NewPostgresRBACStore(&database.Engine{Queries: pgdb.New(pool), DB: pool}), pool
}

func insertTestUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	userID := uuid.NewString()
	_, err := pool.Exec(context.Background(),
		"INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'irrelevant')",
		userID, userID[:8]+"@example.com")
	require.NoError(t, err)
	return userID
}

func insertTestApp(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	appID := uuid.NewString()
	_, err := pool.Exec(context.Background(),
		"INSERT INTO apps (id, name) VALUES ($1, $2)", appID, "app-"+appID[:8])
	require.NoError(t, err)
	return appID
}

func TestRoleCRUDRoundtrip(t *testing.T) {
	rbacStore, _ := setupRBACStore(t)
	ctx := context.Background()

	role, err := rbacStore.InsertRole(ctx, Role{
		ID:          uuid.NewString(),
		Name:        "Release manager " + uuid.NewString()[:8],
		Permissions: []Permission{PermChannelRolloutManage, PermChannelEditBranch},
	})
	require.NoError(t, err)
	require.Equal(t, []Permission{PermChannelRolloutManage, PermChannelEditBranch}, role.Permissions)
	require.False(t, role.CreatedAt.IsZero())

	// The name is UNIQUE.
	_, err = rbacStore.InsertRole(ctx, Role{ID: uuid.NewString(), Name: role.Name})
	alreadyExists := (*store.ErrResourceAlreadyExists)(nil)
	require.True(t, errors.As(err, &alreadyExists), "expected ErrResourceAlreadyExists, got %v", err)

	fetched, err := rbacStore.GetRoleByID(ctx, role.ID)
	require.NoError(t, err)
	require.Equal(t, role.Name, fetched.Name)

	require.NoError(t, rbacStore.UpdateRole(ctx, role.ID, role.Name+" v2", []Permission{PermBranchProtect}))
	fetched, err = rbacStore.GetRoleByID(ctx, role.ID)
	require.NoError(t, err)
	require.Equal(t, role.Name+" v2", fetched.Name)
	require.Equal(t, []Permission{PermBranchProtect}, fetched.Permissions)

	require.NoError(t, rbacStore.DeleteRole(ctx, role.ID))
	require.ErrorIs(t, rbacStore.DeleteRole(ctx, role.ID), ErrRoleNotFound)
	require.ErrorIs(t, rbacStore.UpdateRole(ctx, role.ID, "ghost", nil), ErrRoleNotFound)
	_, err = rbacStore.GetRoleByID(ctx, role.ID)
	require.ErrorIs(t, err, ErrRoleNotFound)
}

func TestReplaceUserGrantsRoundtrip(t *testing.T) {
	rbacStore, pool := setupRBACStore(t)
	ctx := context.Background()

	userID := insertTestUser(t, pool)
	appOne := insertTestApp(t, pool)
	appTwo := insertTestApp(t, pool)
	role, err := rbacStore.InsertRole(ctx, Role{
		ID:          uuid.NewString(),
		Name:        "Ops " + uuid.NewString()[:8],
		Permissions: []Permission{PermBranchProtect},
	})
	require.NoError(t, err)

	require.NoError(t, rbacStore.ReplaceUserGrants(ctx, userID, []GrantInput{
		{AppID: appOne, RoleID: &role.ID, ExtraPermissions: []Permission{PermCertificateRead}},
		{AppID: appTwo},
	}))

	grants, err := rbacStore.ListUserGrants(ctx, userID)
	require.NoError(t, err)
	require.Len(t, grants, 2)
	byApp := map[string]AppGrant{}
	for _, grant := range grants {
		byApp[grant.AppID] = grant
	}
	granted := byApp[appOne]
	require.NotNil(t, granted.RoleID)
	require.Equal(t, role.ID, *granted.RoleID)
	require.NotNil(t, granted.RoleName)
	require.Equal(t, role.Name, *granted.RoleName)
	require.Equal(t, []Permission{PermBranchProtect}, granted.RolePermissions)
	require.Equal(t, []Permission{PermCertificateRead}, granted.ExtraPermissions)
	require.Nil(t, byApp[appTwo].RoleID, "a role-less grant keeps a nil role")

	// The enforcement read resolves the role's permissions too.
	enforcement, err := rbacStore.GetUserAppGrant(ctx, userID, appOne)
	require.NoError(t, err)
	require.NotNil(t, enforcement)
	require.True(t, enforcement.Has(PermBranchProtect))
	require.True(t, enforcement.Has(PermCertificateRead))
	require.False(t, enforcement.Has(PermAppDelete))

	ids, err := rbacStore.ListAccessibleAppIDs(ctx, userID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{appOne, appTwo}, ids)

	// The summary counts this user's grants (the database is shared across
	// tests, so only assert this user's entry).
	counts, err := rbacStore.GrantCountsByUser(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), counts[userID])

	// A role that is assigned cannot be deleted.
	require.ErrorIs(t, rbacStore.DeleteRole(ctx, role.ID), ErrRoleInUse)

	// Replacement is wholesale: the next set fully supersedes the previous one.
	require.NoError(t, rbacStore.ReplaceUserGrants(ctx, userID, []GrantInput{{AppID: appTwo}}))
	grants, err = rbacStore.ListUserGrants(ctx, userID)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.Equal(t, appTwo, grants[0].AppID)
	missing, err := rbacStore.GetUserAppGrant(ctx, userID, appOne)
	require.NoError(t, err)
	require.Nil(t, missing, "no grant reads as nil, not an error")

	// The role is unassigned now, so it can go.
	require.NoError(t, rbacStore.DeleteRole(ctx, role.ID))
}

func TestReplaceUserGrantsRollsBackOnBadReference(t *testing.T) {
	rbacStore, pool := setupRBACStore(t)
	ctx := context.Background()

	userID := insertTestUser(t, pool)
	appID := insertTestApp(t, pool)
	require.NoError(t, rbacStore.ReplaceUserGrants(ctx, userID, []GrantInput{{AppID: appID}}))

	validationErr := (*ValidationError)(nil)

	// Unknown app: refused and the previous grants survive the rollback.
	err := rbacStore.ReplaceUserGrants(ctx, userID, []GrantInput{{AppID: uuid.NewString()}})
	require.True(t, errors.As(err, &validationErr), "expected ValidationError, got %v", err)
	grants, err := rbacStore.ListUserGrants(ctx, userID)
	require.NoError(t, err)
	require.Len(t, grants, 1, "failed replacement must leave the previous grants untouched")

	// Unknown role.
	ghostRole := uuid.NewString()
	err = rbacStore.ReplaceUserGrants(ctx, userID, []GrantInput{{AppID: appID, RoleID: &ghostRole}})
	require.True(t, errors.As(err, &validationErr), "expected ValidationError, got %v", err)

	// Malformed role id.
	badRole := "not-a-uuid"
	err = rbacStore.ReplaceUserGrants(ctx, userID, []GrantInput{{AppID: appID, RoleID: &badRole}})
	require.True(t, errors.As(err, &validationErr), "expected ValidationError, got %v", err)

	// Grants written for a user deleted in the meantime: the user_id FK branch.
	_, err = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	require.NoError(t, err)
	err = rbacStore.ReplaceUserGrants(ctx, userID, []GrantInput{{AppID: appID}})
	require.True(t, errors.As(err, &validationErr), "expected ValidationError, got %v", err)
}

func TestReplaceUserGrantsWithEmptySetClearsEverything(t *testing.T) {
	rbacStore, pool := setupRBACStore(t)
	ctx := context.Background()

	userID := insertTestUser(t, pool)
	appID := insertTestApp(t, pool)
	require.NoError(t, rbacStore.ReplaceUserGrants(ctx, userID, []GrantInput{{AppID: appID}}))

	// The admin removing every access is a plain replacement by nothing.
	require.NoError(t, rbacStore.ReplaceUserGrants(ctx, userID, nil))
	grants, err := rbacStore.ListUserGrants(ctx, userID)
	require.NoError(t, err)
	require.Empty(t, grants)
	ids, err := rbacStore.ListAccessibleAppIDs(ctx, userID)
	require.NoError(t, err)
	require.Empty(t, ids)
}

func TestUpdateRoleRefusesNameCollision(t *testing.T) {
	rbacStore, _ := setupRBACStore(t)
	ctx := context.Background()

	suffix := uuid.NewString()[:8]
	first, err := rbacStore.InsertRole(ctx, Role{ID: uuid.NewString(), Name: "First " + suffix})
	require.NoError(t, err)
	second, err := rbacStore.InsertRole(ctx, Role{ID: uuid.NewString(), Name: "Second " + suffix})
	require.NoError(t, err)

	err = rbacStore.UpdateRole(ctx, second.ID, first.Name, nil)
	alreadyExists := (*store.ErrResourceAlreadyExists)(nil)
	require.True(t, errors.As(err, &alreadyExists), "expected ErrResourceAlreadyExists, got %v", err)

	// The refused rename left the role untouched.
	fetched, err := rbacStore.GetRoleByID(ctx, second.ID)
	require.NoError(t, err)
	require.Equal(t, second.Name, fetched.Name)

	// The listing carries both, sorted by name (the database is shared across
	// tests, so assert relative order rather than the full set).
	roles, err := rbacStore.ListRoles(ctx)
	require.NoError(t, err)
	firstIndex, secondIndex := -1, -1
	for i, role := range roles {
		switch role.ID {
		case first.ID:
			firstIndex = i
		case second.ID:
			secondIndex = i
		}
	}
	require.GreaterOrEqual(t, firstIndex, 0)
	require.GreaterOrEqual(t, secondIndex, 0)
	require.Less(t, firstIndex, secondIndex, "ListRoles must sort by name")
}

func TestGrantsFollowUserAndAppDeletion(t *testing.T) {
	rbacStore, pool := setupRBACStore(t)
	ctx := context.Background()

	userID := insertTestUser(t, pool)
	appID := insertTestApp(t, pool)
	require.NoError(t, rbacStore.ReplaceUserGrants(ctx, userID, []GrantInput{{AppID: appID}}))

	// Deleting the app cascades its grants away.
	_, err := pool.Exec(ctx, "DELETE FROM apps WHERE id = $1", appID)
	require.NoError(t, err)
	ids, err := rbacStore.ListAccessibleAppIDs(ctx, userID)
	require.NoError(t, err)
	require.Empty(t, ids)

	// Same story for the user.
	appID = insertTestApp(t, pool)
	require.NoError(t, rbacStore.ReplaceUserGrants(ctx, userID, []GrantInput{{AppID: appID}}))
	_, err = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	require.NoError(t, err)
	var count int64
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM user_app_grants WHERE user_id = $1", userID).Scan(&count))
	require.Zero(t, count)
}
