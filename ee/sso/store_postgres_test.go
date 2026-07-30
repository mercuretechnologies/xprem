// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Integration tests for the SSO store: the seal/unseal roundtrip of the
// client secret, the provisioning transaction and the identity cascade need a
// real Postgres. They skip unless TEST_DATABASE_URL is set, e.g.:
//
//	docker run -d --name eoo-pg -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:16-alpine
//	TEST_DATABASE_URL="postgres://postgres:test@localhost:55432/postgres?sslmode=disable" go test ./ee/sso/

package sso

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"testing"

	"xprem/internal/database"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testMasterKeyB64(fill byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func setupSSOStore(t *testing.T) (*PostgresSSOStore, *pgxpool.Pool) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		// See the same guard in internal/store/rollout_postgres_test.go: a skip in CI is
		// a green job that ran none of these migrations or queries.
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI: these tests cover SQL that the in-memory fakes cannot reach")
		}
		t.Skip("TEST_DATABASE_URL not set, start a Postgres and set it to run the sso store tests")
	}
	// The seed migration fails fast on an empty database without the
	// bootstrap pair.
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	t.Setenv("DB_KEYS_MASTER_KEY_B64", testMasterKeyB64(7))
	postgres.RunDBMigrations(dbURL)

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return NewPostgresSSOStore(&database.Engine{Queries: pgdb.New(pool), DB: pool}), pool
}

func resetSSOTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, "DELETE FROM sso_identities")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "DELETE FROM sso_config")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "DELETE FROM users")
	require.NoError(t, err)
}

func storedTestConfig() SSOConfig {
	return SSOConfig{
		Issuer:               "https://login.microsoftonline.com/tenant/v2.0",
		ClientID:             "client-id",
		ClientSecret:         "very-confidential",
		ProviderName:         "Microsoft",
		Scopes:               "openid profile email",
		Enabled:              true,
		AllowedEmailDomains:  []string{"acme.com"},
		AllowedGroups:        []string{"eng", "dashboard-users"},
		GroupsClaim:          "groups",
		TrustUnverifiedEmail: true,
	}
}

func TestSSOConfigRoundtrip(t *testing.T) {
	ssoStore, pool := setupSSOStore(t)
	ctx := context.Background()
	resetSSOTables(t, pool)

	// Nothing configured reads as nil, nil: "not configured" is not an error.
	cfg, err := ssoStore.GetConfig(ctx)
	require.NoError(t, err)
	assert.Nil(t, cfg)

	require.NoError(t, ssoStore.SaveConfig(ctx, storedTestConfig()))
	cfg, err = ssoStore.GetConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, storedTestConfig(), *cfg)

	// The secret at rest is sealed, never the plaintext.
	var sealed string
	require.NoError(t, pool.QueryRow(ctx, "SELECT sealed_client_secret FROM sso_config").Scan(&sealed))
	assert.NotContains(t, sealed, "very-confidential")

	// Saving again replaces the singleton row.
	updated := storedTestConfig()
	updated.Enabled = false
	updated.ClientSecret = "rotated-secret"
	updated.AllowedGroups = nil
	require.NoError(t, ssoStore.SaveConfig(ctx, updated))
	cfg, err = ssoStore.GetConfig(ctx)
	require.NoError(t, err)
	assert.False(t, cfg.Enabled)
	assert.Equal(t, "rotated-secret", cfg.ClientSecret)
	assert.Empty(t, cfg.AllowedGroups)
	var rows int
	require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM sso_config").Scan(&rows))
	assert.Equal(t, 1, rows)

	require.NoError(t, ssoStore.DeleteConfig(ctx))
	cfg, err = ssoStore.GetConfig(ctx)
	require.NoError(t, err)
	assert.Nil(t, cfg)
}

func TestSSOConfigSecretUnreadableAfterMasterKeyRotation(t *testing.T) {
	ssoStore, pool := setupSSOStore(t)
	ctx := context.Background()
	resetSSOTables(t, pool)

	require.NoError(t, ssoStore.SaveConfig(ctx, storedTestConfig()))

	// A different master key cannot unseal the stored secret, but the rest of
	// the configuration must survive so the dashboard can prompt for it.
	t.Setenv("DB_KEYS_MASTER_KEY_B64", testMasterKeyB64(8))
	cfg, err := ssoStore.GetConfig(ctx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrClientSecretUnreadable))
	require.NotNil(t, cfg)
	assert.Empty(t, cfg.ClientSecret)
	assert.Equal(t, "client-id", cfg.ClientID)

	// Re-saving under the new key heals the configuration.
	require.NoError(t, ssoStore.SaveConfig(ctx, storedTestConfig()))
	cfg, err = ssoStore.GetConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "very-confidential", cfg.ClientSecret)
}

func TestSSOIdentityLifecycle(t *testing.T) {
	ssoStore, pool := setupSSOStore(t)
	ctx := context.Background()
	resetSSOTables(t, pool)
	issuer := "https://idp.example.com"

	// JIT provisioning: user and identity land atomically.
	// Provisioned disabled, as manual user validation does.
	memberID := uuid.NewString()
	provisioned, err := ssoStore.ProvisionUser(ctx, store.InsertUserParameters{
		ID: memberID, Email: "Member@Acme.com", PasswordHash: "", IsAdmin: false, Enabled: false,
	}, issuer, "subject-1")
	require.NoError(t, err)
	assert.Equal(t, "member@acme.com", provisioned.Email)
	assert.False(t, provisioned.Enabled)

	found, err := ssoStore.FindUserBySubject(ctx, issuer, "subject-1")
	require.NoError(t, err)
	assert.Equal(t, memberID, found.Id)
	assert.Empty(t, found.PasswordHash)
	assert.False(t, found.IsAdmin)
	assert.False(t, found.Enabled)

	// Once an admin approves the account, the next sign-in must see it
	// enabled: reading the flag back is what ends the "pending approval" loop.
	_, err = pool.Exec(ctx, "UPDATE users SET enabled = TRUE WHERE id = $1", memberID)
	require.NoError(t, err)
	found, err = ssoStore.FindUserBySubject(ctx, issuer, "subject-1")
	require.NoError(t, err)
	assert.True(t, found.Enabled)

	// The account's security generation must survive this lookup too. It is
	// what IssueSession stamps into both JWTs, so reading it back as 0 for an
	// account whose sessions were ever revoked would mint a session that the
	// very next request refuses: a sign-in that succeeds and then bounces
	// straight back to the login page, with no way out but a manual UPDATE.
	_, err = pool.Exec(ctx, "UPDATE users SET session_version = 3 WHERE id = $1", memberID)
	require.NoError(t, err)
	found, err = ssoStore.FindUserBySubject(ctx, issuer, "subject-1")
	require.NoError(t, err)
	assert.EqualValues(t, 3, found.SessionVersion)

	// Unknown subject answers the typed not-found error.
	_, err = ssoStore.FindUserBySubject(ctx, issuer, "unknown-subject")
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	assert.ErrorAs(t, err, &notFoundErr)

	// TouchLastLogin records the sign-in.
	require.NoError(t, ssoStore.TouchLastLogin(ctx, issuer, "subject-1"))
	var lastLogin *string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT last_login_at::text FROM sso_identities WHERE issuer = $1 AND subject = $2",
		issuer, "subject-1").Scan(&lastLogin))
	assert.NotNil(t, lastLogin)

	// Linking an existing (password) account to a new subject.
	linkedID := uuid.NewString()
	_, err = pool.Exec(ctx,
		"INSERT INTO users (id, email, password_hash, is_admin) VALUES ($1, $2, 'hash', TRUE)",
		linkedID, "admin@acme.com")
	require.NoError(t, err)
	require.NoError(t, ssoStore.LinkIdentity(ctx, issuer, "subject-2", linkedID, "admin@acme.com"))
	found, err = ssoStore.FindUserBySubject(ctx, issuer, "subject-2")
	require.NoError(t, err)
	assert.Equal(t, linkedID, found.Id)
	assert.True(t, found.IsAdmin)
	assert.True(t, found.Enabled)

	// The same (issuer, subject) cannot be linked twice.
	err = ssoStore.LinkIdentity(ctx, issuer, "subject-2", memberID, "member@acme.com")
	alreadyExistsErr := (*store.ErrResourceAlreadyExists)(nil)
	assert.ErrorAs(t, err, &alreadyExistsErr)

	// Provisioning an already-used email fails atomically: no orphan identity
	// survives the rolled-back transaction.
	_, err = ssoStore.ProvisionUser(ctx, store.InsertUserParameters{
		ID: uuid.NewString(), Email: "member@acme.com",
	}, issuer, "subject-3")
	assert.ErrorAs(t, err, &alreadyExistsErr)
	var orphanCount int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM sso_identities WHERE subject = 'subject-3'").Scan(&orphanCount))
	assert.Equal(t, 0, orphanCount)

	// Deleting the user cascades onto its identities: the next sign-in of
	// that subject re-provisions from scratch.
	_, err = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", memberID)
	require.NoError(t, err)
	_, err = ssoStore.FindUserBySubject(ctx, issuer, "subject-1")
	assert.ErrorAs(t, err, &notFoundErr)
}

// TestSSOMigrationDownAndUp rolls the sso migration back and forward again.
// RunDBMigrations left goose configured (dialect + embedded FS), so DownTo
// can drive the same embedded migrations.
func TestSSOMigrationDownAndUp(t *testing.T) {
	_, pool := setupSSOStore(t)
	ctx := context.Background()
	resetSSOTables(t, pool)
	dbURL := os.Getenv("TEST_DATABASE_URL")

	db, err := sql.Open("pgx", dbURL)
	require.NoError(t, err)
	defer db.Close()

	tableCount := func() int {
		var count int
		require.NoError(t, pool.QueryRow(ctx,
			"SELECT COUNT(*) FROM information_schema.tables WHERE table_name IN ('sso_config', 'sso_identities')").Scan(&count))
		return count
	}

	require.Equal(t, 2, tableCount())
	require.NoError(t, goose.DownTo(db, "migrations", 20260718110000))
	assert.Equal(t, 0, tableCount())
	require.NoError(t, goose.Up(db, "migrations"))
	assert.Equal(t, 2, tableCount())
}
