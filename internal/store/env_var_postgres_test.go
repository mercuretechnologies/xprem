// Integration tests for the env var store: the per-branch unique constraint,
// the branch/app cascades and the scoped delete are enforced by the SQL
// itself.
package store_test

import (
	"context"
	"errors"
	"testing"

	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupEnvVarStore(t *testing.T) (*store.PostgresEnvVarStore, *store.PostgresBranchStore, string, *pgxpool.Pool) {
	t.Helper()
	_, _, pool := setupCredentialsStores(t)
	appId := insertBareApp(t, pool)
	engine := &database.Engine{Queries: pgdb.New(pool), DB: pool}
	return store.NewPostgresEnvVarStore(engine), store.NewPostgresBranchStore(engine), appId, pool
}

func insertBranchRow(t *testing.T, pool *pgxpool.Pool, appId string, name string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		"INSERT INTO branches (app_id, name) VALUES ($1, $2) RETURNING id", appId, name).Scan(&id))
	return id
}

func TestEnvVarUpsertIsScopedPerBranch(t *testing.T) {
	envStore, _, appId, pool := setupEnvVarStore(t)
	ctx := context.Background()
	stagingId := insertBranchRow(t, pool, appId, "staging")
	productionId := insertBranchRow(t, pool, appId, "production")

	require.NoError(t, envStore.UpsertEnvVar(ctx, appId, stagingId, "API_URL", true, "sealed-staging-v1"))
	// The upsert replaces value AND flag: one row per (branch, key).
	require.NoError(t, envStore.UpsertEnvVar(ctx, appId, stagingId, "API_URL", false, "sealed-staging-v2"))
	require.NoError(t, envStore.UpsertEnvVar(ctx, appId, productionId, "API_URL", true, "sealed-production-v1"))

	envVars, err := envStore.ListEnvVars(ctx, appId)
	require.NoError(t, err)
	require.Len(t, envVars, 2)
	for _, row := range envVars {
		if row.BranchName == "staging" {
			assert.False(t, row.IsPublic)
		}
	}

	stagingValue, err := envStore.GetSealedValue(ctx, appId, stagingId, "API_URL")
	require.NoError(t, err)
	require.NotNil(t, stagingValue)
	assert.Equal(t, "sealed-staging-v2", *stagingValue)

	productionValue, err := envStore.GetSealedValue(ctx, appId, productionId, "API_URL")
	require.NoError(t, err)
	require.NotNil(t, productionValue)
	assert.Equal(t, "sealed-production-v1", *productionValue)
}

func TestEnvVarDeleteIsScoped(t *testing.T) {
	envStore, _, appId, pool := setupEnvVarStore(t)
	ctx := context.Background()
	stagingId := insertBranchRow(t, pool, appId, "staging")
	productionId := insertBranchRow(t, pool, appId, "production")

	require.NoError(t, envStore.UpsertEnvVar(ctx, appId, stagingId, "API_URL", true, "sealed-staging"))
	require.NoError(t, envStore.UpsertEnvVar(ctx, appId, productionId, "API_URL", true, "sealed-production"))

	require.NoError(t, envStore.DeleteEnvVar(ctx, appId, stagingId, "API_URL"))
	// The production entry survives the staging-scoped delete.
	productionValue, err := envStore.GetSealedValue(ctx, appId, productionId, "API_URL")
	require.NoError(t, err)
	assert.NotNil(t, productionValue)

	err = envStore.DeleteEnvVar(ctx, appId, stagingId, "API_URL")
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	require.True(t, errors.As(err, &notFoundErr))
}

func TestEnvVarsAreDroppedWithTheirBranch(t *testing.T) {
	envStore, branchStore, appId, pool := setupEnvVarStore(t)
	ctx := context.Background()
	ephemeralId := insertBranchRow(t, pool, appId, "ephemeral")
	keptId := insertBranchRow(t, pool, appId, "kept")
	require.NoError(t, envStore.UpsertEnvVar(ctx, appId, ephemeralId, "API_URL", true, "sealed"))
	require.NoError(t, envStore.UpsertEnvVar(ctx, appId, keptId, "API_URL", true, "sealed"))

	require.NoError(t, branchStore.DeleteBranchByName(ctx, appId, "ephemeral"))

	envVars, err := envStore.ListEnvVars(ctx, appId)
	require.NoError(t, err)
	require.Len(t, envVars, 1)
	assert.Equal(t, "kept", envVars[0].BranchName)
}

