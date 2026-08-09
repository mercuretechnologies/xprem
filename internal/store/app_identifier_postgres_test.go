// Integration tests for the app identifier store: the per-app uniqueness,
// the credentials-guarded delete and the app-deletion cascade are enforced
// by the SQL itself, which the in-memory fakes cannot exercise.
package store_test

import (
	"context"
	"errors"
	"testing"

	"xprem/internal/store"

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
	alreadyExistsErr := (*store.ErrResourceAlreadyExists)(nil)
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

func TestAppIdentifierDeleteIsGuardedByCredentials(t *testing.T) {
	credentialsStore, identifierStore, pool := setupCredentialsStores(t)
	ctx := context.Background()
	appId := insertBareApp(t, pool)
	identifierId := insertIdentifier(t, identifierStore, appId, "android", "com.example.app")
	require.NoError(t, credentialsStore.UpsertAndroidCredentials(ctx, identifierId, sealedFixture("v1")))

	err := identifierStore.DeleteAppIdentifier(ctx, appId, identifierId)
	hasCredsErr := (*store.ErrIdentifierHasCredentials)(nil)
	require.True(t, errors.As(err, &hasCredsErr))
	assert.Equal(t, "com.example.app", hasCredsErr.Identifier)

	require.NoError(t, credentialsStore.DeleteAndroidCredentials(ctx, identifierId))
	require.NoError(t, identifierStore.DeleteAppIdentifier(ctx, appId, identifierId))

	err = identifierStore.DeleteAppIdentifier(ctx, appId, identifierId)
	notFoundErr := (*store.ErrResourceNotFound)(nil)
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
	assert.Equal(t, int64(0), ref.BuildNumber)

	require.NoError(t, identifierStore.SetBuildNumber(ctx, appId, identifierId, 87))
	ref, err = identifierStore.GetAppIdentifierByID(ctx, appId, identifierId)
	require.NoError(t, err)
	assert.Equal(t, int64(87), ref.BuildNumber)

	// Scoped: another app cannot touch the counter.
	otherAppId := insertBareApp(t, pool)
	err = identifierStore.SetBuildNumber(ctx, otherAppId, identifierId, 999)
	notFoundErr := (*store.ErrResourceNotFound)(nil)
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
	notFoundErr := (*store.ErrResourceNotFound)(nil)
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
