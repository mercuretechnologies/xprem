// Integration tests for the environment store: the per-environment unique
// constraints, the cascades, the channel binding and its RESTRICT on delete
// are enforced by the SQL itself.
package repository_test

import (
	"context"
	"errors"
	"testing"

	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/repository"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupEnvironmentStore(t *testing.T) (*repository.PostgresEnvironmentRepository, *repository.PostgresChannelRepository, string, *pgxpool.Pool) {
	t.Helper()
	_, _, pool := setupCredentialsStores(t)
	appId := insertBareApp(t, pool)
	engine := &database.Engine{Queries: pgdb.New(pool), DB: pool}
	return repository.NewPostgresEnvironmentRepository(engine), repository.NewPostgresChannelRepository(engine), appId, pool
}

func insertEnvironment(t *testing.T, envStore *repository.PostgresEnvironmentRepository, appId string, name string) string {
	t.Helper()
	id, err := envStore.InsertEnvironment(context.Background(), appId, name)
	require.NoError(t, err)
	return id
}

func TestEnvironmentNameIsUniquePerApp(t *testing.T) {
	envStore, _, appId, pool := setupEnvironmentStore(t)
	ctx := context.Background()
	otherAppId := insertBareApp(t, pool)

	insertEnvironment(t, envStore, appId, "production")
	_, err := envStore.InsertEnvironment(ctx, appId, "production")
	alreadyExists := (*repository.ErrResourceAlreadyExists)(nil)
	require.True(t, errors.As(err, &alreadyExists))
	// Same name in another app is fine.
	insertEnvironment(t, envStore, otherAppId, "production")

	environments, err := envStore.ListEnvironments(ctx, appId)
	require.NoError(t, err)
	require.Len(t, environments, 1)
	assert.Equal(t, "production", environments[0].Name)

	id, err := envStore.GetEnvironmentIdByName(ctx, appId, "production")
	require.NoError(t, err)
	assert.Equal(t, environments[0].Id, id)
	_, err = envStore.GetEnvironmentIdByName(ctx, otherAppId, "staging")
	notFoundErr := (*repository.ErrResourceNotFound)(nil)
	require.True(t, errors.As(err, &notFoundErr))
}

func TestEnvVarUpsertIsScopedPerEnvironment(t *testing.T) {
	envStore, _, appId, _ := setupEnvironmentStore(t)
	ctx := context.Background()
	stagingId := insertEnvironment(t, envStore, appId, "staging")
	productionId := insertEnvironment(t, envStore, appId, "production")

	require.NoError(t, envStore.UpsertEnvVar(ctx, stagingId, "API_URL", true, "sealed-staging-v1"))
	// The upsert replaces value AND flag: one row per (environment, key).
	require.NoError(t, envStore.UpsertEnvVar(ctx, stagingId, "API_URL", false, "sealed-staging-v2"))
	require.NoError(t, envStore.UpsertEnvVar(ctx, productionId, "API_URL", true, "sealed-production-v1"))

	envVars, err := envStore.ListEnvVars(ctx, appId)
	require.NoError(t, err)
	require.Len(t, envVars, 2)
	for _, row := range envVars {
		if row.EnvironmentId == stagingId {
			assert.False(t, row.IsPublic)
		}
	}

	stagingValue, err := envStore.GetSealedValue(ctx, stagingId, "API_URL")
	require.NoError(t, err)
	require.NotNil(t, stagingValue)
	assert.Equal(t, "sealed-staging-v2", *stagingValue)

	productionValue, err := envStore.GetSealedValue(ctx, productionId, "API_URL")
	require.NoError(t, err)
	require.NotNil(t, productionValue)
	assert.Equal(t, "sealed-production-v1", *productionValue)
}

func TestEnvVarDeleteIsScoped(t *testing.T) {
	envStore, _, appId, _ := setupEnvironmentStore(t)
	ctx := context.Background()
	stagingId := insertEnvironment(t, envStore, appId, "staging")
	productionId := insertEnvironment(t, envStore, appId, "production")

	require.NoError(t, envStore.UpsertEnvVar(ctx, stagingId, "API_URL", true, "sealed-staging"))
	require.NoError(t, envStore.UpsertEnvVar(ctx, productionId, "API_URL", true, "sealed-production"))

	require.NoError(t, envStore.DeleteEnvVar(ctx, stagingId, "API_URL"))
	// The production entry survives the staging-scoped delete.
	productionValue, err := envStore.GetSealedValue(ctx, productionId, "API_URL")
	require.NoError(t, err)
	assert.NotNil(t, productionValue)

	err = envStore.DeleteEnvVar(ctx, stagingId, "API_URL")
	notFoundErr := (*repository.ErrResourceNotFound)(nil)
	require.True(t, errors.As(err, &notFoundErr))
}

func TestEnvVarsAreDroppedWithTheirEnvironment(t *testing.T) {
	envStore, _, appId, _ := setupEnvironmentStore(t)
	ctx := context.Background()
	ephemeralId := insertEnvironment(t, envStore, appId, "ephemeral")
	keptId := insertEnvironment(t, envStore, appId, "kept")
	require.NoError(t, envStore.UpsertEnvVar(ctx, ephemeralId, "API_URL", true, "sealed"))
	require.NoError(t, envStore.UpsertEnvVar(ctx, keptId, "API_URL", true, "sealed"))

	require.NoError(t, envStore.DeleteEnvironment(ctx, appId, "ephemeral"))

	envVars, err := envStore.ListEnvVars(ctx, appId)
	require.NoError(t, err)
	require.Len(t, envVars, 1)
	assert.Equal(t, keptId, envVars[0].EnvironmentId)

	err = envStore.DeleteEnvironment(ctx, appId, "ephemeral")
	notFoundErr := (*repository.ErrResourceNotFound)(nil)
	require.True(t, errors.As(err, &notFoundErr))
}

func TestChannelEnvironmentBinding(t *testing.T) {
	envStore, channelStore, appId, _ := setupEnvironmentStore(t)
	ctx := context.Background()
	productionId := insertEnvironment(t, envStore, appId, "production")
	_, err := channelStore.InsertChannel(ctx, appId, nil, "prod-channel")
	require.NoError(t, err)

	require.NoError(t, envStore.SetChannelEnvironment(ctx, appId, "prod-channel", &productionId))
	channels, err := channelStore.GetChannels(ctx, appId)
	require.NoError(t, err)
	require.Len(t, channels, 1)
	require.NotNil(t, channels[0].EnvironmentName)
	assert.Equal(t, "production", *channels[0].EnvironmentName)

	// A bound environment cannot be deleted.
	err = envStore.DeleteEnvironment(ctx, appId, "production")
	inUseErr := (*repository.ErrEnvironmentHasChannels)(nil)
	require.True(t, errors.As(err, &inUseErr))

	// Unknown channel is a 404.
	err = envStore.SetChannelEnvironment(ctx, appId, "nope", &productionId)
	notFoundErr := (*repository.ErrResourceNotFound)(nil)
	require.True(t, errors.As(err, &notFoundErr))

	// nil unbinds, after which the environment can go.
	require.NoError(t, envStore.SetChannelEnvironment(ctx, appId, "prod-channel", nil))
	channels, err = channelStore.GetChannels(ctx, appId)
	require.NoError(t, err)
	assert.Nil(t, channels[0].EnvironmentName)
	require.NoError(t, envStore.DeleteEnvironment(ctx, appId, "production"))
}

func TestChannelEnvironmentBindingIsScopedToTheApp(t *testing.T) {
	envStore, channelStore, appId, pool := setupEnvironmentStore(t)
	ctx := context.Background()
	otherAppId := insertBareApp(t, pool)
	foreignId := insertEnvironment(t, envStore, otherAppId, "production")
	_, err := channelStore.InsertChannel(ctx, appId, nil, "prod-channel")
	require.NoError(t, err)

	// Another app's environment id is refused even though it exists.
	err = envStore.SetChannelEnvironment(ctx, appId, "prod-channel", &foreignId)
	notFoundErr := (*repository.ErrResourceNotFound)(nil)
	require.True(t, errors.As(err, &notFoundErr))
	channels, err := channelStore.GetChannels(ctx, appId)
	require.NoError(t, err)
	assert.Nil(t, channels[0].EnvironmentName)
}

func TestEnvVarUpsertOnDeletedEnvironmentIsNotFound(t *testing.T) {
	envStore, _, appId, _ := setupEnvironmentStore(t)
	ctx := context.Background()
	goneId := insertEnvironment(t, envStore, appId, "gone")
	require.NoError(t, envStore.DeleteEnvironment(ctx, appId, "gone"))

	err := envStore.UpsertEnvVar(ctx, goneId, "API_URL", true, "sealed")
	notFoundErr := (*repository.ErrResourceNotFound)(nil)
	require.True(t, errors.As(err, &notFoundErr))
}

func TestResolveEnvironmentVariablesSelectorsAndAppScope(t *testing.T) {
	envStore, channels, appID, pool := setupEnvironmentStore(t)
	ctx := context.Background()
	otherAppID := insertBareApp(t, pool)
	selectedID := insertEnvironment(t, envStore, appID, "staging")
	foreignID := insertEnvironment(t, envStore, otherAppID, "staging")
	emptyID := insertEnvironment(t, envStore, appID, "empty")
	for _, target := range []struct{ app, id, value string }{{appID, selectedID, "selected"}, {otherAppID, foreignID, "foreign"}} {
		_, err := channels.InsertChannel(ctx, target.app, nil, "release")
		require.NoError(t, err)
		require.NoError(t, envStore.SetChannelEnvironment(ctx, target.app, "release", &target.id))
		require.NoError(t, envStore.UpsertEnvVar(ctx, target.id, "TOKEN", true, target.value))
		for _, selector := range []struct{ channel, env string }{{"release", ""}, {"", "staging"}} {
			resolved, err := envStore.ResolveEnvironmentVariables(ctx, target.app, selector.channel, selector.env)
			require.NoError(t, err)
			require.Equal(t, target.id, resolved.ID)
			require.Equal(t, "staging", *resolved.Name)
			require.Equal(t, []repository.SealedEnvVar{{Key: "TOKEN", IsPublic: true, SealedValue: target.value}}, resolved.Variables)
		}
	}
	_, err := channels.InsertChannel(ctx, appID, nil, "unbound")
	require.NoError(t, err)
	resolved, err := envStore.ResolveEnvironmentVariables(ctx, appID, "unbound", "")
	require.NoError(t, err)
	require.Nil(t, resolved.Name)
	require.Empty(t, resolved.Variables)
	resolved, err = envStore.ResolveEnvironmentVariables(ctx, appID, "", "empty")
	require.NoError(t, err)
	require.Equal(t, emptyID, resolved.ID)
	require.Equal(t, "empty", *resolved.Name)
	require.Empty(t, resolved.Variables)
	for _, target := range []struct{ app, channel, env string }{{otherAppID, "unbound", ""}, {otherAppID, "", "empty"}, {appID, "missing", ""}, {appID, "", "missing"}} {
		resolved, err := envStore.ResolveEnvironmentVariables(ctx, target.app, target.channel, target.env)
		require.Nil(t, resolved)
		var missing *repository.ErrResourceNotFound
		require.ErrorAs(t, err, &missing)
	}
}
