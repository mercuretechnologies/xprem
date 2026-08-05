// Integration tests for the Android credentials store: the one-row-per-app
// upsert and the not-found delete are enforced by the SQL itself, which the
// in-memory fakes cannot exercise. Same TEST_DATABASE_URL contract as the
// branch store tests.
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupCredentialsStore(t *testing.T) (*store.PostgresCredentialsStore, *pgxpool.Pool) {
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
	return store.NewPostgresCredentialsStore(&database.Engine{Queries: pgdb.New(pool), DB: pool}), pool
}

func insertBareApp(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	appId := uuid.NewString()
	_, err := pool.Exec(context.Background(), "INSERT INTO apps (id, name) VALUES ($1, $2)", appId, "app-"+appId[:8])
	require.NoError(t, err)
	return appId
}

func sealedFixture(marker string) store.SealedAndroidCredentials {
	return store.SealedAndroidCredentials{
		AndroidPackage:         "com.example." + marker,
		KeyAlias:               "upload",
		SealedKeystore:         "sealed-keystore-" + marker,
		SealedKeystorePassword: "sealed-keystore-password-" + marker,
		SealedKeyPassword:      "sealed-key-password-" + marker,
	}
}

func TestAndroidCredentialsUpsertReplacesTheSingleRow(t *testing.T) {
	credentialsStore, pool := setupCredentialsStore(t)
	ctx := context.Background()
	appId := insertBareApp(t, pool)

	require.NoError(t, credentialsStore.UpsertAndroidCredentials(ctx, appId, sealedFixture("v1")))
	gsa := "sealed-gsa-v2"
	second := sealedFixture("v2")
	second.SealedGoogleServiceAccountKey = &gsa
	require.NoError(t, credentialsStore.UpsertAndroidCredentials(ctx, appId, second))

	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM android_credentials WHERE app_id = $1", appId).Scan(&count))
	assert.Equal(t, 1, count)

	stored, err := credentialsStore.GetAndroidCredentials(ctx, appId)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "com.example.v2", stored.AndroidPackage)
	assert.Equal(t, "sealed-keystore-v2", stored.SealedKeystore)
	require.NotNil(t, stored.SealedGoogleServiceAccountKey)
	assert.Equal(t, "sealed-gsa-v2", *stored.SealedGoogleServiceAccountKey)
}

func TestAndroidCredentialsGetReturnsNilWhenAbsent(t *testing.T) {
	credentialsStore, pool := setupCredentialsStore(t)
	appId := insertBareApp(t, pool)

	stored, err := credentialsStore.GetAndroidCredentials(context.Background(), appId)
	require.NoError(t, err)
	assert.Nil(t, stored)
}

func TestAndroidCredentialsDeleteReportsNotFound(t *testing.T) {
	credentialsStore, pool := setupCredentialsStore(t)
	ctx := context.Background()
	appId := insertBareApp(t, pool)

	err := credentialsStore.DeleteAndroidCredentials(ctx, appId)
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	require.True(t, errors.As(err, &notFoundErr))

	require.NoError(t, credentialsStore.UpsertAndroidCredentials(ctx, appId, sealedFixture("v1")))
	require.NoError(t, credentialsStore.DeleteAndroidCredentials(ctx, appId))
	stored, err := credentialsStore.GetAndroidCredentials(ctx, appId)
	require.NoError(t, err)
	assert.Nil(t, stored)
}

func TestAndroidCredentialsRowsAreDroppedWithTheApp(t *testing.T) {
	credentialsStore, pool := setupCredentialsStore(t)
	ctx := context.Background()
	appId := insertBareApp(t, pool)
	require.NoError(t, credentialsStore.UpsertAndroidCredentials(ctx, appId, sealedFixture("v1")))

	_, err := pool.Exec(ctx, "DELETE FROM apps WHERE id = $1", appId)
	require.NoError(t, err)

	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM android_credentials WHERE app_id = $1", appId).Scan(&count))
	assert.Equal(t, 0, count)
}
