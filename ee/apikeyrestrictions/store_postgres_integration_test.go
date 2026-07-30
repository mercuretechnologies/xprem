// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Integration tests for per-token access against a real Postgres.
//
// They skip unless TEST_DATABASE_URL is set, e.g.:
//
//	docker run -d --name eoo-pg -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:16-alpine
//	TEST_DATABASE_URL="postgres://postgres:test@localhost:55432/postgres?sslmode=disable" go test ./ee/apikeyrestrictions/

package apikeyrestrictions

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"testing"

	"xprem/internal/database"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/services"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupAccessStore(t *testing.T) (*PostgresApiKeyAccessStore, *pgxpool.Pool) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		// A skip in CI would be a green job that ran none of these queries.
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI: these tests cover SQL that the in-memory fakes cannot reach")
		}
		t.Skip("TEST_DATABASE_URL not set, start a Postgres and set it to run the api key access store tests")
	}
	// The seed migration fails fast on an empty database without the
	// bootstrap pair.
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(dbURL)

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return NewPostgresApiKeyAccessStore(&database.Engine{Queries: pgdb.New(pool), DB: pool}), pool
}

func insertTestApp(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	appID := uuid.NewString()
	_, err := pool.Exec(context.Background(),
		"INSERT INTO apps (id, name) VALUES ($1, $2)", appID, "app-"+appID[:8])
	require.NoError(t, err)
	return appID
}

func insertTestBranch(t *testing.T, pool *pgxpool.Pool, appID, branchName string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		"INSERT INTO branches (app_id, name) VALUES ($1, $2)", appID, branchName)
	require.NoError(t, err)
}

// insertTestApiKey returns the key's id. hashed_key is UNIQUE and CHAR(64), so
// each key gets a distinct 64-character filler.
func insertTestApiKey(t *testing.T, pool *pgxpool.Pool, appID, name string) int64 {
	t.Helper()
	hashed := fmt.Sprintf("%-64s", uuid.NewString()+uuid.NewString())[:64]
	var apiKeyID int64
	err := pool.QueryRow(context.Background(),
		"INSERT INTO api_keys (app_id, name, hint, hashed_key) VALUES ($1, $2, 'eoo_***', $3) RETURNING id",
		appID, name, hashed).Scan(&apiKeyID)
	require.NoError(t, err)
	return apiKeyID
}

func TestAccessRoundTripsThroughPostgres(t *testing.T) {
	store, pool := setupAccessStore(t)
	ctx := context.Background()
	appID := insertTestApp(t, pool)
	apiKeyID := insertTestApiKey(t, pool, appID, "ci")

	// A fresh key is unrestricted: one row comes back, with no rule.
	access, err := store.GetAccess(ctx, apiKeyID)
	require.NoError(t, err)
	assert.Empty(t, access.BranchRules)
	assert.Empty(t, access.AllowedIps)

	require.NoError(t, store.SetAccess(ctx, appID, ApiKeyAccess{
		ApiKeyID:   apiKeyID,
		AllowedIps: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		BranchRules: []BranchRule{
			{Pattern: "production", Actions: []Action{ActionRead}},
			{Pattern: "pr-*", Actions: []Action{ActionRead, ActionPublish}},
		},
	}))

	access, err = store.GetAccess(ctx, apiKeyID)
	require.NoError(t, err)
	assert.Equal(t, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, access.AllowedIps)
	require.Len(t, access.BranchRules, 2)
	assert.ElementsMatch(t,
		[]string{"production", "pr-*"},
		[]string{access.BranchRules[0].Pattern, access.BranchRules[1].Pattern})

	// Rules are replaced wholesale, not merged: the two above are gone.
	require.NoError(t, store.SetAccess(ctx, appID, ApiKeyAccess{
		ApiKeyID:    apiKeyID,
		BranchRules: []BranchRule{{Pattern: "staging", Actions: []Action{ActionPublish}}},
	}))
	access, err = store.GetAccess(ctx, apiKeyID)
	require.NoError(t, err)
	require.Len(t, access.BranchRules, 1)
	assert.Equal(t, "staging", access.BranchRules[0].Pattern)
	assert.Empty(t, access.AllowedIps)
}

// The app listing folds many rows into one entry per key, and keys still at
// their default have to appear too.
func TestGetAccessByAppIDFoldsRealRows(t *testing.T) {
	store, pool := setupAccessStore(t)
	ctx := context.Background()
	appID := insertTestApp(t, pool)
	scopedID := insertTestApiKey(t, pool, appID, "scoped")
	defaultID := insertTestApiKey(t, pool, appID, "default")

	require.NoError(t, store.SetAccess(ctx, appID, ApiKeyAccess{
		ApiKeyID: scopedID,
		BranchRules: []BranchRule{
			{Pattern: "a", Actions: []Action{ActionRead}},
			{Pattern: "b", Actions: []Action{ActionPublish}},
			{Pattern: "c", Actions: []Action{ActionRollback}},
		},
	}))

	accesses, err := store.GetAccessByAppID(ctx, appID)
	require.NoError(t, err)
	byID := map[int64]ApiKeyAccess{}
	for _, access := range accesses {
		byID[access.ApiKeyID] = access
	}
	require.Contains(t, byID, scopedID)
	require.Contains(t, byID, defaultID)
	assert.Len(t, byID[scopedID].BranchRules, 3)
	assert.Empty(t, byID[defaultID].BranchRules)
}

// SetAccess is one transaction; a key from another app is rejected before its
// rules are touched.
func TestSetAccessRejectsAKeyOfAnotherApp(t *testing.T) {
	store, pool := setupAccessStore(t)
	ctx := context.Background()
	appID := insertTestApp(t, pool)
	otherAppID := insertTestApp(t, pool)
	apiKeyID := insertTestApiKey(t, pool, appID, "ci")

	require.NoError(t, store.SetAccess(ctx, appID, ApiKeyAccess{
		ApiKeyID:    apiKeyID,
		BranchRules: []BranchRule{{Pattern: "staging", Actions: []Action{ActionPublish}}},
	}))

	err := store.SetAccess(ctx, otherAppID, ApiKeyAccess{
		ApiKeyID:    apiKeyID,
		BranchRules: []BranchRule{{Pattern: "production", Actions: []Action{ActionPublish}}},
	})
	require.ErrorIs(t, err, ErrApiKeyNotFound)

	// The rule from the accepted call is still there, untouched.
	access, err := store.GetAccess(ctx, apiKeyID)
	require.NoError(t, err)
	require.Len(t, access.BranchRules, 1)
	assert.Equal(t, "staging", access.BranchRules[0].Pattern)
}

// The enforcement read refuses a revoked key rather than answering with its rules.
func TestGetAccessRefusesARevokedKey(t *testing.T) {
	store, pool := setupAccessStore(t)
	ctx := context.Background()
	appID := insertTestApp(t, pool)
	apiKeyID := insertTestApiKey(t, pool, appID, "ci")
	require.NoError(t, store.SetAccess(ctx, appID, ApiKeyAccess{
		ApiKeyID:    apiKeyID,
		BranchRules: []BranchRule{{Pattern: "staging", Actions: []Action{ActionPublish}}},
	}))

	_, err := pool.Exec(ctx, "UPDATE api_keys SET revoked_at = CURRENT_TIMESTAMP WHERE id = $1", apiKeyID)
	require.NoError(t, err)

	_, err = store.GetAccess(ctx, apiKeyID)
	require.ErrorIs(t, err, ErrApiKeyNotFound)
}

// Deleting a key must cascade to its rules.
func TestDeletingAKeyCascadesToItsRules(t *testing.T) {
	store, pool := setupAccessStore(t)
	ctx := context.Background()
	appID := insertTestApp(t, pool)
	apiKeyID := insertTestApiKey(t, pool, appID, "ci")
	require.NoError(t, store.SetAccess(ctx, appID, ApiKeyAccess{
		ApiKeyID:    apiKeyID,
		BranchRules: []BranchRule{{Pattern: "staging", Actions: []Action{ActionPublish}}},
	}))

	_, err := pool.Exec(ctx, "DELETE FROM api_keys WHERE id = $1", apiKeyID)
	require.NoError(t, err)

	var remaining int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM api_key_branch_rules WHERE api_key_id = $1", apiKeyID).Scan(&remaining))
	assert.Zero(t, remaining)
}

// The whole decision, service included, over the real schema.
func TestAuthorizeAgainstPostgres(t *testing.T) {
	store, pool := setupAccessStore(t)
	ctx := context.Background()
	appID := insertTestApp(t, pool)
	apiKeyID := insertTestApiKey(t, pool, appID, "ci")
	insertTestBranch(t, pool, appID, "staging")

	service := serviceWith(store, true)
	require.NoError(t, service.SetAccess(ctx, appID, apiKeyID,
		[]BranchRule{
			{Pattern: "production", Actions: []Action{ActionRead}},
			{Pattern: "pr-*", Actions: []Action{ActionPublish}},
			{Pattern: "staging", Actions: []Action{ActionPublish}},
		},
		[]string{"10.0.0.0/8"},
	))

	request := func(branch string, action Action, ip string) CliRequest {
		return CliRequest{
			AppID:    appID,
			APIKeyID: apiKeyID,
			Branch:   branch,
			Action:   action,
			ClientIP: netip.MustParseAddr(ip),
		}
	}

	// Allowed: an existing branch the rules cover, from an allowed address.
	require.NoError(t, service.Authorize(ctx, request("staging", ActionPublish, "10.1.2.3")))
	// Both writes imply read.
	require.NoError(t, service.Authorize(ctx, request("staging", ActionRead, "10.1.2.3")))

	// Refused: the rule on production grants read only.
	err := service.Authorize(ctx, request("production", ActionPublish, "10.1.2.3"))
	require.ErrorIs(t, err, services.ErrCliAccessDenied)

	// Refused: no rule covers this branch at all.
	err = service.Authorize(ctx, request("develop", ActionPublish, "10.1.2.3"))
	require.ErrorIs(t, err, services.ErrCliAccessDenied)

	// Allowed although pr-482 does not exist yet: the rule admits the name.
	require.NoError(t, service.Authorize(ctx, request("pr-482", ActionPublish, "10.1.2.3")))

	// Refused: right branch, right action, wrong address.
	err = service.Authorize(ctx, request("staging", ActionPublish, "203.0.113.9"))
	require.ErrorIs(t, err, ErrIpNotAllowed)

	// Nothing is enforced without a license.
	community := serviceWith(store, false)
	require.NoError(t, community.Authorize(ctx, request("develop", ActionPublish, "203.0.113.9")))
}

// A repository failure must not read as a bad credential: the CLI maps
// ErrCliAuthUnavailable to a 500 and everything else to a 401.
func TestAuthorizeReportsAnUnreachableControlPlane(t *testing.T) {
	store, pool := setupAccessStore(t)
	ctx := context.Background()
	appID := insertTestApp(t, pool)
	apiKeyID := insertTestApiKey(t, pool, appID, "ci")
	service := serviceWith(store, true)

	pool.Close()

	err := service.Authorize(ctx, CliRequest{AppID: appID, APIKeyID: apiKeyID, Branch: "staging", Action: ActionPublish})
	require.Error(t, err)
	assert.True(t, errors.Is(err, services.ErrCliAuthUnavailable),
		"a database failure must be reported as unverifiable, got %v", err)
	assert.False(t, errors.Is(err, services.ErrCliAccessDenied),
		"a database failure must not read as a refusal")
}
