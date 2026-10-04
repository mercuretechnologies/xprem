// Integration tests for the app identifier store: the per-app uniqueness,
// the identifier and app deletion cascades are enforced
// by the SQL itself, which the in-memory fakes cannot exercise.
package repository_test

import (
	"context"
	"errors"
	"testing"
	"xprem/internal/services"
	"xprem/internal/types"

	"xprem/internal/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppIdentifierUniquePerAppPlatformIdentifier(t *testing.T) {
	_, identifierStore, pool := setupCredentialsStores(t)
	ctx := context.Background()
	appId := insertBareApp(t, pool)

	_, err := identifierStore.InsertAppIdentifier(ctx, appId, "android", "com.example.app")
	require.NoError(t, err)
	// Same identifier on the other platform is a distinct identity.
	_, err = identifierStore.InsertAppIdentifier(ctx, appId, "ios", "com.example.app")
	require.NoError(t, err)

	_, err = identifierStore.InsertAppIdentifier(ctx, appId, "android", "com.example.app")
	alreadyExistsErr := (*repository.ErrResourceAlreadyExists)(nil)
	require.True(t, errors.As(err, &alreadyExistsErr))

	// The same identifier under another app is fine.
	otherAppId := insertBareApp(t, pool)
	_, err = identifierStore.InsertAppIdentifier(ctx, otherAppId, "android", "com.example.app")
	require.NoError(t, err)
}

func TestAppIdentifierListReportsCredentialState(t *testing.T) {
	credentialsStore, identifierStore, pool := setupCredentialsStores(t)
	ctx := context.Background()
	appId := insertBareApp(t, pool)
	withCreds := insertIdentifier(t, identifierStore, appId, "android", "com.example.app")
	insertIdentifier(t, identifierStore, appId, "android", "com.example.staging")
	require.NoError(t, credentialsStore.UpsertAndroidCredentials(ctx, withCreds, sealedFixture("v1")))

	identifiers, err := identifierStore.GetAppIdentifiers(ctx, appId)
	require.NoError(t, err)
	require.Len(t, identifiers, 2)
	byIdentifier := map[string]bool{}
	for _, row := range identifiers {
		byIdentifier[row.Identifier] = row.HasAndroidCredentials
	}
	assert.True(t, byIdentifier["com.example.app"])
	assert.False(t, byIdentifier["com.example.staging"])
}

func TestAppIdentifierDeleteRemovesCredentials(t *testing.T) {
	credentialsStore, identifierStore, pool := setupCredentialsStores(t)
	ctx := context.Background()
	appId := insertBareApp(t, pool)
	identifierId := insertIdentifier(t, identifierStore, appId, "android", "com.example.app")
	require.NoError(t, credentialsStore.UpsertAndroidCredentials(ctx, identifierId, sealedFixture("v1")))

	require.NoError(t, identifierStore.DeleteAppIdentifier(ctx, appId, identifierId))
	credentials, err := credentialsStore.GetAndroidCredentials(ctx, identifierId)
	require.NoError(t, err)
	assert.Nil(t, credentials)

	err = identifierStore.DeleteAppIdentifier(ctx, appId, identifierId)
	notFoundErr := (*repository.ErrResourceNotFound)(nil)
	require.True(t, errors.As(err, &notFoundErr))
}

func TestAppIdentifierBuildNumberDefaultsAndSets(t *testing.T) {
	_, identifierStore, pool := setupCredentialsStores(t)
	ctx := context.Background()
	appId := insertBareApp(t, pool)
	identifierId := insertIdentifier(t, identifierStore, appId, "android", "com.example.app")

	ref, err := identifierStore.GetAppIdentifierByID(ctx, appId, identifierId)
	require.NoError(t, err)
	require.NotNil(t, ref)
	assert.Equal(t, "0", ref.BuildNumber)

	require.NoError(t, identifierStore.SetBuildNumber(ctx, appId, identifierId, "87"))
	ref, err = identifierStore.GetAppIdentifierByID(ctx, appId, identifierId)
	require.NoError(t, err)
	assert.Equal(t, "87", ref.BuildNumber)

	// Scoped: another app cannot touch the counter.
	otherAppId := insertBareApp(t, pool)
	err = identifierStore.SetBuildNumber(ctx, otherAppId, identifierId, "999")
	notFoundErr := (*repository.ErrResourceNotFound)(nil)
	require.True(t, errors.As(err, &notFoundErr))
}

func TestAppIdentifierScopedToItsApp(t *testing.T) {
	_, identifierStore, pool := setupCredentialsStores(t)
	ctx := context.Background()
	appId := insertBareApp(t, pool)
	otherAppId := insertBareApp(t, pool)
	identifierId := insertIdentifier(t, identifierStore, appId, "android", "com.example.app")

	ref, err := identifierStore.GetAppIdentifierByID(ctx, otherAppId, identifierId)
	require.NoError(t, err)
	assert.Nil(t, ref)

	err = identifierStore.DeleteAppIdentifier(ctx, otherAppId, identifierId)
	notFoundErr := (*repository.ErrResourceNotFound)(nil)
	require.True(t, errors.As(err, &notFoundErr))
}

func TestAppIdentifierRowsAreDroppedWithTheApp(t *testing.T) {
	credentialsStore, identifierStore, pool := setupCredentialsStores(t)
	ctx := context.Background()
	appId := insertBareApp(t, pool)
	identifierId := insertIdentifier(t, identifierStore, appId, "android", "com.example.app")
	require.NoError(t, credentialsStore.UpsertAndroidCredentials(ctx, identifierId, sealedFixture("v1")))

	_, err := pool.Exec(ctx, "DELETE FROM apps WHERE id = $1", appId)
	require.NoError(t, err)

	var identifierCount, credentialsCount int
	require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM app_identifiers WHERE app_id = $1", appId).Scan(&identifierCount))
	require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM android_credentials WHERE app_identifier_id = $1", identifierId).Scan(&credentialsCount))
	assert.Equal(t, 0, identifierCount)
	assert.Equal(t, 0, credentialsCount)
}

func TestAllocateBuildNumberIsScopedAndBounded(t *testing.T) {
	_, identifiers, pool := setupCredentialsStores(t)
	ctx := context.Background()
	service := services.NewAppIdentifierService(identifiers)
	app := insertBareApp(t, pool)
	otherApp := insertBareApp(t, pool)
	id := insertIdentifier(t, identifiers, app, "android", "com.example.counter")

	first, err := service.AllocateBuildNumber(ctx, app, id)
	require.NoError(t, err)
	require.Equal(t, "1", first)
	second, err := service.AllocateBuildNumber(ctx, app, id)
	require.NoError(t, err)
	require.Equal(t, "2", second)

	_, err = service.AllocateBuildNumber(ctx, otherApp, id)
	var missing *repository.ErrResourceNotFound
	require.ErrorAs(t, err, &missing)

	require.NoError(t, identifiers.SetBuildNumber(ctx, app, id, "2099999999"))
	last, err := service.AllocateBuildNumber(ctx, app, id)
	require.NoError(t, err)
	require.Equal(t, "2100000000", last)
	_, err = service.AllocateBuildNumber(ctx, app, id)
	require.ErrorIs(t, err, repository.ErrBuildNumberExhausted)

	require.NoError(t, identifiers.SetBuildNumber(ctx, app, id, "4"))
	next, err := service.AllocateBuildNumber(ctx, app, id)
	require.NoError(t, err)
	require.Equal(t, "5", next)
}

func TestAppIdentifierLookupByPlatformAndIdentifier(t *testing.T) {
	_, repo, pool := setupCredentialsStores(t)
	ctx := context.Background()
	app := insertBareApp(t, pool)
	other := insertBareApp(t, pool)
	android := insertIdentifier(t, repo, app, "android", "com.example.app")
	ios := insertIdentifier(t, repo, app, "ios", "com.example.app")
	foreign := insertIdentifier(t, repo, other, "android", "com.example.app")
	require.NoError(t, repo.SetBuildNumber(ctx, app, android, "42"))
	for _, tc := range []struct{ app, platform, identifier, id string }{
		{app, "android", "com.example.app", android},
		{app, "ios", "com.example.app", ios},
		{other, "android", "com.example.app", foreign},
		{other, "ios", "com.example.app", ""},
		{app, "android", "com.example.missing", ""},
	} {
		ref, err := repo.GetAppIdentifierByPlatformAndIdentifier(ctx, tc.app, types.Platform(tc.platform), tc.identifier)
		require.NoError(t, err)
		if tc.id == "" {
			require.Nil(t, ref)
			continue
		}
		require.NotNil(t, ref)
		require.Equal(t, tc.id, ref.Id)
		require.Equal(t, types.Platform(tc.platform), ref.Platform)
		require.Equal(t, tc.identifier, ref.Identifier)
		if tc.id == android {
			require.Equal(t, "42", ref.BuildNumber)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := repo.GetAppIdentifierByPlatformAndIdentifier(cancelled, app, types.PlatformAndroid, "com.example.app")
	require.ErrorIs(t, err, context.Canceled)
}
