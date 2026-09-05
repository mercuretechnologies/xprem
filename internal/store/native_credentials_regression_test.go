package store_test

import (
	"context"
	"encoding/base64"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
	"xprem/internal/android/androidtest"
	"xprem/internal/crypto"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/services"
	"xprem/internal/store"
)

func TestVaultUUIDAliasesRoundTrip(t *testing.T) {
	credentialsStore, identifiers, pool := setupCredentialsStores(t)
	ctx := context.Background()
	appId := insertBareApp(t, pool)
	identifierId := insertIdentifier(t, identifiers, appId, "android", "com.review.uuid")
	master := []byte("0123456789abcdef0123456789abcdef")
	t.Setenv("AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID", "")
	t.Setenv("DB_KEYS_MASTER_KEY_B64", base64.StdEncoding.EncodeToString(master))
	service := services.NewCredentialsService(credentialsStore, identifiers)
	input := services.AndroidCredentialsInput{KeyAlias: "upload", KeystoreBase64: base64.StdEncoding.EncodeToString(androidtest.JKSKeystore("store-pass", "key-pass", "upload")), KeystorePassword: "store-pass", KeyPassword: "key-pass"}
	require.NoError(t, service.SaveAndroidCredentials(ctx, appId, strings.ToUpper(identifierId), input))
	stored, err := credentialsStore.GetAndroidCredentials(ctx, identifierId)
	require.NoError(t, err)
	require.NotNil(t, stored)
	_, err = crypto.UnsealAESGCM(stored.SealedKeystore, master, []byte(identifierId+"|android_credentials|keystore"))
	if err != nil {
		t.Errorf("credential saved through accepted UUID spelling cannot be decrypted with canonical database ID: %v", err)
	}
}

func TestDeleteIdentifierPreservesConcurrentCredentials(t *testing.T) {
	_, identifiers, pool := setupCredentialsStores(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	appId := insertBareApp(t, pool)
	identifierId := insertIdentifier(t, identifiers, appId, "android", "com.review.race")
	insertTx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer insertTx.Rollback(context.Background())
	_, err = insertTx.Exec(ctx, "INSERT INTO android_credentials (id, app_identifier_id, key_alias, sealed_keystore, sealed_keystore_password, sealed_key_password) VALUES (gen_random_uuid(), $1, 'upload', 'sealed', 'sealed', 'sealed')", identifierId)
	require.NoError(t, err)
	deleteConn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer deleteConn.Release()
	deletePID := deleteConn.Conn().PgConn().PID()
	deletionStore := store.NewPostgresAppIdentifierStore(&database.Engine{Queries: pgdb.New(deleteConn), DB: &identifierTestConnection{deleteConn}})
	done := make(chan error, 1)
	go func() { done <- deletionStore.DeleteAppIdentifier(ctx, appId, identifierId) }()
	require.Eventually(t, func() bool {
		var blocked bool
		err := pool.QueryRow(ctx, "SELECT COALESCE(wait_event_type = 'Lock', false) FROM pg_stat_activity WHERE pid = $1", deletePID).Scan(&blocked)
		return err == nil && blocked
	}, 5*time.Second, 10*time.Millisecond, "delete must be blocked behind credentials insert before commit")
	require.NoError(t, insertTx.Commit(ctx))
	deleteErr := <-done
	var remaining int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM android_credentials WHERE app_identifier_id=$1", identifierId).Scan(&remaining))
	t.Logf("delete error=%v, credentials remaining=%d", deleteErr, remaining)
	if deleteErr == nil || remaining != 1 {
		t.Error("guarded delete erased credentials committed by concurrent upload")
	}
}

func TestDeleteAppWithBoundEnvironment(t *testing.T) {
	environments, channels, appId, pool := setupEnvironmentStore(t)
	ctx := context.Background()
	envId := insertEnvironment(t, environments, appId, "production")
	_, err := channels.InsertChannel(ctx, appId, nil, "production")
	require.NoError(t, err)
	require.NoError(t, environments.SetChannelEnvironment(ctx, appId, "production", &envId))
	_, err = pool.Exec(ctx, "DELETE FROM apps WHERE id=$1", appId)
	require.NoError(t, err)
}

// Pin both transactional and nontransactional operations to the observed backend.
type identifierTestConnection struct{ *pgxpool.Conn }

func (c *identifierTestConnection) Close() { c.Release() }
