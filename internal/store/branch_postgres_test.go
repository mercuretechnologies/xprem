// Integration tests for the guarded branch delete: the "protected branches
// cannot be deleted" rule is enforced inside the DELETE statement itself,
// which the in-memory fakes cannot exercise. They need a real Postgres and
// skip unless TEST_DATABASE_URL is set, e.g.:
//
//	docker run -d --name eoo-pg -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:16-alpine
//	TEST_DATABASE_URL="postgres://postgres:test@localhost:55432/postgres?sslmode=disable" go test ./internal/store/
//
// The package is store_test to avoid an import cycle (store -> database/postgres -> migrations -> store).
package store_test

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

func setupBranchStore(t *testing.T) (*store.PostgresBranchStore, *pgxpool.Pool) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI: these tests cover SQL that the in-memory fakes cannot reach")
		}
		t.Skip("TEST_DATABASE_URL not set, start a Postgres and set it to run the guarded-query tests")
	}
	// The seed migration fails fast on an empty database without the bootstrap pair.
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(dbURL)

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return store.NewPostgresBranchStore(&database.Engine{Queries: pgdb.New(pool), DB: pool}), pool
}

func insertAppWithBranch(t *testing.T, pool *pgxpool.Pool, branchName string, protected bool) string {
	t.Helper()
	ctx := context.Background()
	appId := uuid.NewString()
	_, err := pool.Exec(ctx, "INSERT INTO apps (id, name) VALUES ($1, $2)", appId, "app-"+appId[:8])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "INSERT INTO branches (app_id, name, protected) VALUES ($1, $2, $3)", appId, branchName, protected)
	require.NoError(t, err)
	return appId
}

func branchExists(t *testing.T, pool *pgxpool.Pool, appId string, branchName string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT EXISTS (SELECT 1 FROM branches WHERE app_id = $1 AND name = $2)", appId, branchName).Scan(&exists))
	return exists
}

func TestDeleteBranchRefusesProtectedBranch(t *testing.T) {
	branchStore, pool := setupBranchStore(t)
	ctx := context.Background()

	appId := insertAppWithBranch(t, pool, "production", true)

	err := branchStore.DeleteBranchByName(ctx, appId, "production")
	protectedErr := (*store.ErrBranchProtected)(nil)
	require.True(t, errors.As(err, &protectedErr), "expected ErrBranchProtected, got %v", err)
	require.True(t, branchExists(t, pool, appId, "production"), "the protected branch must survive the delete attempt")

	// Lifting the protection unblocks the delete.
	_, err = pool.Exec(ctx, "UPDATE branches SET protected = FALSE WHERE app_id = $1 AND name = $2", appId, "production")
	require.NoError(t, err)
	require.NoError(t, branchStore.DeleteBranchByName(ctx, appId, "production"))
	require.False(t, branchExists(t, pool, appId, "production"))
}

func TestDeleteBranchStillReportsMissingBranch(t *testing.T) {
	branchStore, pool := setupBranchStore(t)
	ctx := context.Background()

	appId := insertAppWithBranch(t, pool, "staging", false)

	err := branchStore.DeleteBranchByName(ctx, appId, "does-not-exist")
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	require.True(t, errors.As(err, &notFoundErr), "expected ErrResourceNotFound, got %v", err)
	require.True(t, branchExists(t, pool, appId, "staging"))
}

// TestGetBranchesCarriesTheProtectionFlag verifies the listing reports each
// branch's actual protected flag, not a constant.
func TestGetBranchesCarriesTheProtectionFlag(t *testing.T) {
	branchStore, pool := setupBranchStore(t)
	appId := insertAppWithBranch(t, pool, "production", true)
	_, err := pool.Exec(context.Background(),
		"INSERT INTO branches (app_id, name, protected) VALUES ($1, $2, FALSE)", appId, "staging")
	require.NoError(t, err)

	branches, err := branchStore.GetBranches(context.Background(), appId)
	require.NoError(t, err)

	byName := map[string]bool{}
	for _, branch := range branches {
		byName[branch.BranchName] = branch.Protected
	}
	require.Len(t, byName, 2)
	require.True(t, byName["production"], "a protected branch must be reported as protected")
	require.False(t, byName["staging"], "an unprotected branch must not be reported as protected")
}
