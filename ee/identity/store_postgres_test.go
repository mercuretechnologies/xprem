// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Integration tests for the identity store: the merge-under-lock transaction,
// the value-stat bookkeeping and the trigram search need a real Postgres.
// They skip unless TEST_DATABASE_URL is set, e.g.:
//
//	docker run -d --name eoo-pg -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:16-alpine
//	TEST_DATABASE_URL="postgres://postgres:test@localhost:55432/postgres?sslmode=disable" go test ./ee/identity/
//
// Every test creates its own app row, so tests never observe each other's
// devices or stats even on a database reused across runs.

package identity

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres"
	"expo-open-ota/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func setupIdentityStore(t *testing.T) (*PostgresIdentityStore, *pgxpool.Pool) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		// Same guard as the audit and rbac store tests: a skip in CI is a
		// green job that ran none of these queries.
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI: these tests cover SQL that unit tests cannot reach")
		}
		t.Skip("TEST_DATABASE_URL not set; start a Postgres and set it to run the identity store tests")
	}
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(dbURL)

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := NewPostgresIdentityStore(&database.Engine{Queries: pgdb.New(pool), DB: pool})
	return store, pool
}

func seedApp(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	appID := uuid.NewString()
	_, err := pool.Exec(context.Background(), "INSERT INTO apps (id, name) VALUES ($1, $2)", appID, "identity-test-"+appID[:8])
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM apps WHERE id = $1", appID)
	})
	return appID
}

func seedPublishedUpdate(t *testing.T, pool *pgxpool.Pool, appID, updateID string) {
	t.Helper()
	ctx := context.Background()
	suffix := updateID[:8]
	var branchID, runtimeVersionID int64
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO branches (app_id, name) VALUES ($1, $2) RETURNING id",
		appID, "health-"+suffix).Scan(&branchID))
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO runtime_versions (app_id, version) VALUES ($1, $2) RETURNING id",
		appID, "health-"+suffix).Scan(&runtimeVersionID))
	_, err := pool.Exec(ctx, `
		INSERT INTO updates
			(id, update_uuid, branch_id, runtime_version_id, update_type, commit_hash, platform, checked_at)
		VALUES (1, $1, $2, $3, 0, 'health-test', 'ios', CURRENT_TIMESTAMP)`,
		updateID, branchID, runtimeVersionID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM branches WHERE id = $1", branchID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM runtime_versions WHERE id = $1", runtimeVersionID)
	})
}

// Same fixture as seedPublishedUpdate, with an explicit publish group and a
// row id of its own so several updates can coexist.
func seedGroupedUpdate(t *testing.T, pool *pgxpool.Pool, appID, updateID, publishGroup string, rowID int64) {
	t.Helper()
	ctx := context.Background()
	suffix := updateID[:8]
	var branchID, runtimeVersionID int64
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO branches (app_id, name) VALUES ($1, $2) RETURNING id",
		appID, "group-"+suffix).Scan(&branchID))
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO runtime_versions (app_id, version) VALUES ($1, $2) RETURNING id",
		appID, "group-"+suffix).Scan(&runtimeVersionID))
	_, err := pool.Exec(ctx, `
		INSERT INTO updates
			(id, update_uuid, branch_id, runtime_version_id, update_type, commit_hash, platform, checked_at, publish_group)
		VALUES ($5, $1, $2, $3, 0, 'group-test', 'android', CURRENT_TIMESTAMP, $4)`,
		updateID, branchID, runtimeVersionID, publishGroup, rowID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM branches WHERE id = $1", branchID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM runtime_versions WHERE id = $1", runtimeVersionID)
	})
}

func declareKey(t *testing.T, store *PostgresIdentityStore, appID, key string, valueType ValueType) {
	t.Helper()
	_, err := store.UpsertSchemaKey(context.Background(), appID, KeySpec{Key: key, Type: valueType, MaxLength: DefaultMaxLength})
	require.NoError(t, err)
}

func TestSchemaCRUD(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()

	schema, err := store.GetSchema(ctx, appID)
	require.NoError(t, err)
	require.Empty(t, schema)

	_, err = store.UpsertSchemaKey(ctx, appID, KeySpec{Key: "userId", Type: ValueTypeString})
	require.NoError(t, err)
	_, err = store.UpsertSchemaKey(ctx, appID, KeySpec{Key: "seats", Type: ValueTypeNumber, MaxLength: 32})
	require.NoError(t, err)

	schema, err = store.GetSchema(ctx, appID)
	require.NoError(t, err)
	require.Len(t, schema, 2)
	// Omitted max length lands on the default, not on zero.
	require.Equal(t, DefaultMaxLength, schema["userId"].MaxLength)
	require.Equal(t, 32, schema["seats"].MaxLength)

	// Upsert re-types a key in place.
	_, err = store.UpsertSchemaKey(ctx, appID, KeySpec{Key: "seats", Type: ValueTypeString, MaxLength: 32})
	require.NoError(t, err)
	schema, err = store.GetSchema(ctx, appID)
	require.NoError(t, err)
	require.Equal(t, ValueTypeString, schema["seats"].Type)

	// Invalid specs are rejected before touching the database.
	_, err = store.UpsertSchemaKey(ctx, appID, KeySpec{Key: "bad key", Type: ValueTypeString})
	require.Error(t, err)

	deleted, err := store.DeleteSchemaKey(ctx, appID, "seats")
	require.NoError(t, err)
	require.True(t, deleted)
	deleted, err = store.DeleteSchemaKey(ctx, appID, "seats")
	require.NoError(t, err)
	require.False(t, deleted)
}

func TestApplySetMergesAndCounts(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	declareKey(t, store, appID, "userId", ValueTypeString)
	declareKey(t, store, appID, "tenant", ValueTypeString)

	clientID := uuid.NewString()
	result, err := store.ApplySet(ctx, appID, clientID, map[string]any{
		"userId": "user_1",
		"junk":   "dropped by the allowlist",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"userId": "user_1"}, result.Device.Metadata)
	require.Equal(t, []string{"junk"}, result.DroppedKeys)

	// Second identify adds a key and keeps the first one (per-key merge).
	result, err = store.ApplySet(ctx, appID, clientID, map[string]any{"tenant": "acme"}, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"userId": "user_1", "tenant": "acme"}, result.Device.Metadata)

	// Changing a value moves the device count from the old value to the new
	// one and prunes the emptied row.
	_, err = store.ApplySet(ctx, appID, clientID, map[string]any{"tenant": "globex"}, nil)
	require.NoError(t, err)
	values, err := store.SearchMetadataValues(ctx, appID, "tenant", "", 10)
	require.NoError(t, err)
	require.Equal(t, []ValueCount{{Value: "globex", DeviceCount: 1}}, values)

	// Re-identifying the same value must not inflate the count.
	_, err = store.ApplySet(ctx, appID, clientID, map[string]any{"tenant": "globex"}, nil)
	require.NoError(t, err)
	values, err = store.SearchMetadataValues(ctx, appID, "tenant", "", 10)
	require.NoError(t, err)
	require.Equal(t, []ValueCount{{Value: "globex", DeviceCount: 1}}, values)

	device, err := store.GetDevice(ctx, appID, clientID)
	require.NoError(t, err)
	require.NotNil(t, device)
	require.Equal(t, "globex", device.Metadata["tenant"])

	missing, err := store.GetDevice(ctx, appID, uuid.NewString())
	require.NoError(t, err)
	require.Nil(t, missing)

	_, err = store.ApplySet(ctx, appID, "not-a-uuid", map[string]any{}, nil)
	require.Error(t, err)
}

func strPtr(s string) *string     { return &s }
func floatPtr(f float64) *float64 { return &f }

func TestApplySetGeoCoalesce(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	clientID := uuid.NewString()

	fullGeo := &Geo{CountryCode: strPtr("FR"), City: strPtr("Paris"), Lat: floatPtr(48.85), Lng: floatPtr(2.35)}
	result, err := store.ApplySet(ctx, appID, clientID, nil, fullGeo)
	require.NoError(t, err)
	require.NotNil(t, result.Device.CountryCode)
	require.Equal(t, "FR", *result.Device.CountryCode)

	// An identify that resolves no geo keeps the previously known location.
	result, err = store.ApplySet(ctx, appID, clientID, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Device.CountryCode)
	require.Equal(t, "FR", *result.Device.CountryCode)
	require.NotNil(t, result.Device.Lat)
	require.InDelta(t, 48.85, *result.Device.Lat, 0.001)

	// A PARTIAL resolution (country-only is the common GeoLite2 case) updates
	// what it knows and never blanks the rest with '' or 0/0.
	result, err = store.ApplySet(ctx, appID, clientID, nil, &Geo{CountryCode: strPtr("BE")})
	require.NoError(t, err)
	require.Equal(t, "BE", *result.Device.CountryCode)
	require.NotNil(t, result.Device.City)
	require.Equal(t, "Paris", *result.Device.City)
	require.NotNil(t, result.Device.Lat)
	require.InDelta(t, 48.85, *result.Device.Lat, 0.001)
}

func TestSearchMetadataValuesRankingAndFilter(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	declareKey(t, store, appID, "tenant", ValueTypeString)

	seed := map[string]int{"acme": 3, "acme-eu": 2, "globex": 1}
	for tenant, devices := range seed {
		for i := 0; i < devices; i++ {
			_, err := store.ApplySet(ctx, appID, uuid.NewString(), map[string]any{"tenant": tenant}, nil)
			require.NoError(t, err)
		}
	}

	// Empty search: top values by device count.
	values, err := store.SearchMetadataValues(ctx, appID, "tenant", "", 10)
	require.NoError(t, err)
	require.Equal(t, []ValueCount{{Value: "acme", DeviceCount: 3}, {Value: "acme-eu", DeviceCount: 2}, {Value: "globex", DeviceCount: 1}}, values)

	// Case-insensitive substring narrows, ranking is preserved.
	values, err = store.SearchMetadataValues(ctx, appID, "tenant", "ACME", 10)
	require.NoError(t, err)
	require.Equal(t, []ValueCount{{Value: "acme", DeviceCount: 3}, {Value: "acme-eu", DeviceCount: 2}}, values)

	// Limit applies after ranking.
	values, err = store.SearchMetadataValues(ctx, appID, "tenant", "", 1)
	require.NoError(t, err)
	require.Equal(t, []ValueCount{{Value: "acme", DeviceCount: 3}}, values)

	// Unknown key: no rows, no error.
	values, err = store.SearchMetadataValues(ctx, appID, "nope", "", 10)
	require.NoError(t, err)
	require.Empty(t, values)
}

func TestDeleteSchemaKeyWipesItsStats(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	declareKey(t, store, appID, "tenant", ValueTypeString)
	declareKey(t, store, appID, "plan", ValueTypeString)

	_, err := store.ApplySet(ctx, appID, uuid.NewString(), map[string]any{"tenant": "acme", "plan": "pro"}, nil)
	require.NoError(t, err)

	deleted, err := store.DeleteSchemaKey(ctx, appID, "tenant")
	require.NoError(t, err)
	require.True(t, deleted)

	// The removed key stops being suggested; the surviving key is untouched.
	values, err := store.SearchMetadataValues(ctx, appID, "tenant", "", 10)
	require.NoError(t, err)
	require.Empty(t, values)
	values, err = store.SearchMetadataValues(ctx, appID, "plan", "", 10)
	require.NoError(t, err)
	require.Equal(t, []ValueCount{{Value: "pro", DeviceCount: 1}}, values)

	// And its values are no longer accepted on the next identify.
	result, err := store.ApplySet(ctx, appID, uuid.NewString(), map[string]any{"tenant": "acme"}, nil)
	require.NoError(t, err)
	require.Empty(t, result.Device.Metadata)
	require.Equal(t, []string{"tenant"}, result.DroppedKeys)
}

// Concurrent first identifies of the same install must both land: the
// insert-then-lock sequence serializes the merges, so neither metadata write
// nor stat increment is lost.
func TestApplySetConcurrentFirstWrite(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	declareKey(t, store, appID, "userId", ValueTypeString)
	declareKey(t, store, appID, "tenant", ValueTypeString)

	clientID := uuid.NewString()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = store.ApplySet(ctx, appID, clientID, map[string]any{"userId": "user_1"}, nil)
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = store.ApplySet(ctx, appID, clientID, map[string]any{"tenant": "acme"}, nil)
	}()
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	device, err := store.GetDevice(ctx, appID, clientID)
	require.NoError(t, err)
	require.NotNil(t, device)
	require.Equal(t, map[string]any{"userId": "user_1", "tenant": "acme"}, device.Metadata)

	// The stat increments must survive the serialization too: a lost or
	// double-counted increment would pass a metadata-only assertion.
	values, err := store.SearchMetadataValues(ctx, appID, "userId", "", 10)
	require.NoError(t, err)
	require.Equal(t, []ValueCount{{Value: "user_1", DeviceCount: 1}}, values)
	values, err = store.SearchMetadataValues(ctx, appID, "tenant", "", 10)
	require.NoError(t, err)
	require.Equal(t, []ValueCount{{Value: "acme", DeviceCount: 1}}, values)
}

// Two identifies of DIFFERENT devices sharing stat rows (same tenant/plan
// values) must not deadlock: the store orders its stat-row locks by
// (key, value) precisely for this. Before that ordering, this test deadlocked
// within a handful of iterations (40P01 after the 1s deadlock_timeout).
func TestApplySetConcurrentSharedStatRowsNoDeadlock(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	declareKey(t, store, appID, "tenant", ValueTypeString)
	declareKey(t, store, appID, "plan", ValueTypeString)
	declareKey(t, store, appID, "region", ValueTypeString)

	deviceA, deviceB := uuid.NewString(), uuid.NewString()
	payload := map[string]any{"tenant": "acme", "plan": "pro", "region": "eu"}
	// Alternating payload so decrements and increments cross between rounds.
	alternate := map[string]any{"tenant": "globex", "plan": "free", "region": "us"}

	const rounds = 40
	var wg sync.WaitGroup
	errsA := make([]error, rounds)
	errsB := make([]error, rounds)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			p := payload
			if i%2 == 1 {
				p = alternate
			}
			if _, err := store.ApplySet(ctx, appID, deviceA, p, nil); err != nil {
				errsA[i] = err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			p := alternate
			if i%2 == 1 {
				p = payload
			}
			if _, err := store.ApplySet(ctx, appID, deviceB, p, nil); err != nil {
				errsB[i] = err
				return
			}
		}
	}()
	wg.Wait()
	for i := 0; i < rounds; i++ {
		require.NoError(t, errsA[i], "device A round %d", i)
		require.NoError(t, errsB[i], "device B round %d", i)
	}

	// Both devices ran an even number of rounds, so A ends on `alternate` and
	// B ends on `payload`: every value should count exactly one device.
	for key, want := range map[string][]ValueCount{
		"tenant": {{Value: "acme", DeviceCount: 1}, {Value: "globex", DeviceCount: 1}},
		"plan":   {{Value: "free", DeviceCount: 1}, {Value: "pro", DeviceCount: 1}},
		"region": {{Value: "eu", DeviceCount: 1}, {Value: "us", DeviceCount: 1}},
	} {
		values, err := store.SearchMetadataValues(ctx, appID, key, "", 10)
		require.NoError(t, err)
		require.ElementsMatch(t, want, values, "key %s", key)
	}
}

// A number-typed key must round-trip through JSONB without corrupting the
// stat bookkeeping: 42 stored then re-read as float64 must compare equal to
// an incoming 42 (no phantom dec/inc), and a real change must move the count.
func TestApplySetNumberRoundtrip(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	declareKey(t, store, appID, "seats", ValueTypeNumber)

	clientID := uuid.NewString()
	_, err := store.ApplySet(ctx, appID, clientID, map[string]any{"seats": int64(42)}, nil)
	require.NoError(t, err)
	_, err = store.ApplySet(ctx, appID, clientID, map[string]any{"seats": float64(42)}, nil)
	require.NoError(t, err)
	values, err := store.SearchMetadataValues(ctx, appID, "seats", "", 10)
	require.NoError(t, err)
	require.Equal(t, []ValueCount{{Value: "42", DeviceCount: 1}}, values)

	_, err = store.ApplySet(ctx, appID, clientID, map[string]any{"seats": 42.5}, nil)
	require.NoError(t, err)
	values, err = store.SearchMetadataValues(ctx, appID, "seats", "", 10)
	require.NoError(t, err)
	require.Equal(t, []ValueCount{{Value: "42.5", DeviceCount: 1}}, values)
}

func TestApplySetOnce(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	declareKey(t, store, appID, "initialReferrer", ValueTypeString)
	declareKey(t, store, appID, "plan", ValueTypeString)

	clientID := uuid.NewString()
	result, err := store.ApplySetOnce(ctx, appID, clientID, map[string]any{"initialReferrer": "organic"}, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"initialReferrer": "organic"}, result.Device.Metadata)

	// A second set_once on a held key is silently ignored; absent keys apply.
	result, err = store.ApplySetOnce(ctx, appID, clientID, map[string]any{"initialReferrer": "paid", "plan": "pro"}, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"initialReferrer": "organic", "plan": "pro"}, result.Device.Metadata)

	// The ignored write must not have touched the stats either.
	values, err := store.SearchMetadataValues(ctx, appID, "initialReferrer", "", 10)
	require.NoError(t, err)
	require.Equal(t, []ValueCount{{Value: "organic", DeviceCount: 1}}, values)

	// $set still overwrites what $set_once pinned.
	result, err = store.ApplySet(ctx, appID, clientID, map[string]any{"initialReferrer": "paid"}, nil)
	require.NoError(t, err)
	require.Equal(t, "paid", result.Device.Metadata["initialReferrer"])
}

func TestApplyUnset(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	declareKey(t, store, appID, "userId", ValueTypeString)
	declareKey(t, store, appID, "tenant", ValueTypeString)

	clientID := uuid.NewString()
	_, err := store.ApplySet(ctx, appID, clientID, map[string]any{"userId": "user_1", "tenant": "acme"}, nil)
	require.NoError(t, err)
	// A second device holds the same userId value so the count sits at 2:
	// without the payload dedupe, the duplicated key below would decrement
	// twice, hit zero, and wrongly prune this survivor's count.
	survivor := uuid.NewString()
	_, err = store.ApplySet(ctx, appID, survivor, map[string]any{"userId": "user_1"}, nil)
	require.NoError(t, err)

	// Unset removes the key, decrements its stat once, and ignores
	// duplicated and unknown keys in the payload.
	result, err := store.ApplyUnset(ctx, appID, clientID, []string{"userId", "userId", "neverSeen"}, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"tenant": "acme"}, result.Device.Metadata)
	values, err := store.SearchMetadataValues(ctx, appID, "userId", "", 10)
	require.NoError(t, err)
	require.Equal(t, []ValueCount{{Value: "user_1", DeviceCount: 1}}, values)

	// Unsetting the survivor takes the count to zero and prunes the row.
	_, err = store.ApplyUnset(ctx, appID, survivor, []string{"userId"}, nil)
	require.NoError(t, err)
	values, err = store.SearchMetadataValues(ctx, appID, "userId", "", 10)
	require.NoError(t, err)
	require.Empty(t, values)
	values, err = store.SearchMetadataValues(ctx, appID, "tenant", "", 10)
	require.NoError(t, err)
	require.Equal(t, []ValueCount{{Value: "acme", DeviceCount: 1}}, values)

	// Unset still works for a key removed from the allowlist: cleanup path.
	deleted, err := store.DeleteSchemaKey(ctx, appID, "tenant")
	require.NoError(t, err)
	require.True(t, deleted)
	result, err = store.ApplyUnset(ctx, appID, clientID, []string{"tenant"}, nil)
	require.NoError(t, err)
	require.Empty(t, result.Device.Metadata)

	// Unsetting on a never-seen device just creates the empty row, no error.
	fresh := uuid.NewString()
	result, err = store.ApplyUnset(ctx, appID, fresh, []string{"userId"}, nil)
	require.NoError(t, err)
	require.Empty(t, result.Device.Metadata)
}

func TestUpsertSchemaKeyCap(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()

	for i := 0; i < MaxSchemaKeys; i++ {
		_, err := store.UpsertSchemaKey(ctx, appID, KeySpec{Key: fmt.Sprintf("key%d", i), Type: ValueTypeString})
		require.NoError(t, err)
	}
	// The 101st key is rejected with the typed sentinel...
	_, err := store.UpsertSchemaKey(ctx, appID, KeySpec{Key: "overflow", Type: ValueTypeString})
	require.ErrorIs(t, err, ErrTooManySchemaKeys)
	// ...but re-declaring an existing key at the cap still works.
	_, err = store.UpsertSchemaKey(ctx, appID, KeySpec{Key: "key0", Type: ValueTypeNumber})
	require.NoError(t, err)
}

func TestListDevicesPaginationAndFilter(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	declareKey(t, store, appID, "tenant", ValueTypeString)

	// Seed devices with staggered last_seen_at (later ApplySet = more recent).
	var ids []string
	for i := 0; i < 5; i++ {
		id := uuid.NewString()
		ids = append(ids, id)
		tenant := "acme"
		if i%2 == 1 {
			tenant = "globex"
		}
		_, err := store.ApplySet(ctx, appID, id, map[string]any{"tenant": tenant}, nil)
		require.NoError(t, err)
	}

	// Full unfiltered listing, newest-first, paginated 2 at a time.
	var seen []string
	var cursor *DeviceCursor
	for {
		devices, next, err := store.ListDevices(ctx, appID, DeviceQuery{}, 2, cursor)
		require.NoError(t, err)
		for _, d := range devices {
			seen = append(seen, d.EASClientID)
		}
		if next == nil {
			break
		}
		cursor = next
		require.LessOrEqual(t, len(seen), 5, "pagination must terminate")
	}
	require.Len(t, seen, 5)
	// Newest-first: the last-seeded device comes first.
	require.Equal(t, ids[4], seen[0])
	// No duplicates across pages.
	require.Len(t, uniqueStrings(seen), 5)

	// Filter to tenant=globex (devices 1 and 3): 2 of them.
	filtered, next, err := store.ListDevices(ctx, appID, DeviceQuery{Metadata: MetadataFilters{{Key: "tenant", Values: []any{"globex"}}}}, 10, nil)
	require.NoError(t, err)
	require.Nil(t, next)
	require.Len(t, filtered, 2)
	for _, d := range filtered {
		require.Equal(t, "globex", d.Metadata["tenant"])
	}

	// A filter matching nothing returns an empty page.
	none, _, err := store.ListDevices(ctx, appID, DeviceQuery{Metadata: MetadataFilters{{Key: "tenant", Values: []any{"nope"}}}}, 10, nil)
	require.NoError(t, err)
	require.Empty(t, none)
}

// Containment against JSONB is type-aware, so a boolean or a number has to
// reach the store as one: filtering `canary` with the string "true" matches a
// row stored as `true` in no database.
func TestListDevicesFilterOnTypedValues(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	declareKey(t, store, appID, "canary", ValueTypeBoolean)
	declareKey(t, store, appID, "planLevel", ValueTypeNumber)

	for i := 0; i < 4; i++ {
		_, err := store.ApplySet(ctx, appID, uuid.NewString(), map[string]any{
			"canary":    i%2 == 1,
			"planLevel": float64(i),
		}, nil)
		require.NoError(t, err)
	}

	canaries, _, err := store.ListDevices(ctx, appID, DeviceQuery{Metadata: MetadataFilters{{Key: "canary", Values: []any{true}}}}, 10, nil)
	require.NoError(t, err)
	require.Len(t, canaries, 2)

	level, _, err := store.ListDevices(ctx, appID, DeviceQuery{Metadata: MetadataFilters{{Key: "planLevel", Values: []any{float64(2)}}}}, 10, nil)
	require.NoError(t, err)
	require.Len(t, level, 1)

	// Several values on one key is a union, which is how a comparison across
	// two plans reaches the store.
	levels, _, err := store.ListDevices(ctx, appID, DeviceQuery{
		Metadata: MetadataFilters{{Key: "planLevel", Values: []any{float64(2), float64(3)}}},
	}, 10, nil)
	require.NoError(t, err)
	require.Len(t, levels, 2)

	// The same filter spelled as text, which is what the bug was.
	asText, _, err := store.ListDevices(ctx, appID, DeviceQuery{Metadata: MetadataFilters{{Key: "canary", Values: []any{"true"}}}}, 10, nil)
	require.NoError(t, err)
	require.Empty(t, asText)
}

// When many devices share the exact same last_seen_at (the likely case: a
// burst of identifies), pagination must fall back on the eas_client_id
// tiebreaker and still return every row once. Sequential ApplySet calls get
// distinct timestamps, so force a tie with a direct UPDATE.
func TestListDevicesKeysetUnderTies(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()

	for i := 0; i < 6; i++ {
		_, err := store.ApplySet(ctx, appID, uuid.NewString(), map[string]any{}, nil)
		require.NoError(t, err)
	}
	// Pin all six rows to the same instant.
	_, err := pool.Exec(ctx,
		"UPDATE device_identity SET last_seen_at = '2026-07-23T10:00:00Z' WHERE app_id = $1", appID)
	require.NoError(t, err)

	var seen []string
	var cursor *DeviceCursor
	for {
		devices, next, err := store.ListDevices(ctx, appID, DeviceQuery{}, 2, cursor)
		require.NoError(t, err)
		for _, d := range devices {
			seen = append(seen, d.EASClientID)
		}
		if next == nil {
			break
		}
		cursor = next
		require.LessOrEqual(t, len(seen), 6, "pagination must terminate under ties")
	}
	// All six, each exactly once, despite identical last_seen_at.
	require.Len(t, seen, 6)
	require.Len(t, uniqueStrings(seen), 6)
}

func uniqueStrings(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

func TestTouchDeviceRegistersAndBumps(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()

	// First contact registers the device; the registry is uncapped.
	deviceID := uuid.NewString()
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, nil, DeviceInfo{}))
	created, err := store.GetDevice(ctx, appID, deviceID)
	require.NoError(t, err)
	require.NotNil(t, created)

	// A later contact bumps last_seen, never touching metadata.
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, nil, DeviceInfo{}))
	bumped, err := store.GetDevice(ctx, appID, deviceID)
	require.NoError(t, err)
	require.NotNil(t, bumped)
	require.False(t, bumped.LastSeenAt.Before(created.LastSeenAt))
	require.Empty(t, bumped.Metadata)
}

func TestTouchDeviceGeoCoalesce(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()

	deviceID := uuid.NewString()
	country := "FR"
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, &Geo{CountryCode: &country}, nil, DeviceInfo{}))

	// A later contact resolving no geo must not erase the known one.
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, nil, DeviceInfo{}))
	device, err := store.GetDevice(ctx, appID, deviceID)
	require.NoError(t, err)
	require.NotNil(t, device)
	require.NotNil(t, device.CountryCode)
	require.Equal(t, "FR", *device.CountryCode)
}

// Two first contacts of the same brand-new device race: both must succeed
// (the loser lands on RegisterDevice's ON CONFLICT bump) and exactly one row
// may exist. This is the contract the uncapped registry keeps.
func TestTouchDeviceConcurrentSameDevice(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	deviceID := uuid.NewString()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, nil, DeviceInfo{}))
		}()
	}
	wg.Wait()

	var rows int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM device_identity WHERE app_id = $1 AND eas_client_id = $2", appID, deviceID).Scan(&rows))
	require.Equal(t, 1, rows)
}

func TestTouchDeviceTracksCurrentUpdate(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	deviceID := uuid.NewString()
	updateA, updateB := uuid.NewString(), uuid.NewString()

	// Registration carries the running update.
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, &updateA, DeviceInfo{}))
	var current *string
	readCurrent := func() *string {
		t.Helper()
		var v *string
		require.NoError(t, pool.QueryRow(ctx,
			"SELECT current_update_id::text FROM device_identity WHERE app_id = $1 AND eas_client_id = $2", appID, deviceID).Scan(&v))
		return v
	}
	current = readCurrent()
	require.NotNil(t, current)
	require.Equal(t, updateA, *current)

	// A contact that does not know (nil) keeps the known value.
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, nil, DeviceInfo{}))
	current = readCurrent()
	require.NotNil(t, current)
	require.Equal(t, updateA, *current)

	// A transition overwrites it.
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, &updateB, DeviceInfo{}))
	current = readCurrent()
	require.NotNil(t, current)
	require.Equal(t, updateB, *current)

	// The history bridge records state transitions, not every contact. The
	// nil contact above therefore emits nothing.
	type stateEvent struct {
		EventType        string
		UpdateID         string
		PreviousUpdateID *string
	}
	rows, err := pool.Query(ctx, `
		SELECT event_type, update_id::text, previous_update_id::text
		FROM device_health_outbox
		WHERE app_id = $1 AND eas_client_id = $2
		ORDER BY id`, appID, deviceID)
	require.NoError(t, err)
	defer rows.Close()
	var events []stateEvent
	for rows.Next() {
		var event stateEvent
		require.NoError(t, rows.Scan(&event.EventType, &event.UpdateID, &event.PreviousUpdateID))
		events = append(events, event)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []stateEvent{
		{EventType: "first_seen", UpdateID: updateA},
		{EventType: "switched", UpdateID: updateB, PreviousUpdateID: &updateA},
	}, events)
}

func TestRecordUpdateFailuresCaptureOnce(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	deviceID := uuid.NewString()
	failedUpdate := uuid.NewString()
	seedPublishedUpdate(t, pool, appID, failedUpdate)

	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, nil, DeviceInfo{}))

	// The crash poll captures the fatal error...
	require.NoError(t, store.RecordUpdateFailures(ctx, appID, deviceID, []string{failedUpdate}, "TypeError: boom", FailureTypeUpdate))
	// ...sticky re-sends carry no error and must not blank it, nor duplicate.
	require.NoError(t, store.RecordUpdateFailures(ctx, appID, deviceID, []string{failedUpdate}, "", FailureTypeUpdate))
	// The other source is a row of its own since failure_type joined the key
	// (20260726140000_failure_type_in_key.sql): a runtime crash reported for the
	// same pair must not land on the rollback's row and retype it.
	require.NoError(t, store.RecordUpdateFailures(ctx, appID, deviceID, []string{failedUpdate}, "different error", FailureTypeRuntime))
	// Forged ids in the list are skipped without failing.
	require.NoError(t, store.RecordUpdateFailures(ctx, appID, deviceID, []string{"garbage", failedUpdate}, "", FailureTypeUpdate))

	var rows int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM device_update_failures WHERE app_id = $1 AND update_id = $2", appID, failedUpdate).Scan(&rows))
	require.Equal(t, 2, rows, "one row per (device, update, failure_type); sticky re-sends of each collapse")

	// Capture-once holds within each type: the first non-empty error wins and a
	// later re-send neither blanks nor replaces it.
	var fatal string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT fatal_error FROM device_update_failures WHERE app_id = $1 AND update_id = $2 AND failure_type = $3",
		appID, failedUpdate, string(FailureTypeUpdate)).Scan(&fatal))
	require.Equal(t, "TypeError: boom", fatal)
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT fatal_error FROM device_update_failures WHERE app_id = $1 AND update_id = $2 AND failure_type = $3",
		appID, failedUpdate, string(FailureTypeRuntime)).Scan(&fatal))
	require.Equal(t, "different error", fatal)

	var outboxRows int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM device_health_outbox
		WHERE app_id = $1
		  AND eas_client_id = $2
		  AND update_id = $3
		  AND event_type = 'failure'`,
		appID, deviceID, failedUpdate).Scan(&outboxRows))
	// One per failure KIND, not per re-send: the four calls above insert two
	// rows and re-send onto them. Both events carry the same
	// (app, device, update), the triple update_crashes is keyed on, so they
	// collapse to one row downstream.
	require.Equal(t, 2, outboxRows, "replayed failure headers must emit one historical event per kind")
}

func TestRuntimeFailureRecoveryUsesEventTime(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	deviceID := uuid.NewString()
	updateID := uuid.NewString()
	seedPublishedUpdate(t, pool, appID, updateID)
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, &updateID, DeviceInfo{}))

	crashedAt := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Millisecond)
	require.NoError(t, store.RecordRuntimeFailure(
		ctx, appID, deviceID, updateID, "TypeError: boom", crashedAt,
	))

	// A startup at the same instant cannot prove that a later JS session ran:
	// crash wins ties.
	require.NoError(t, store.ResolveRuntimeFailure(ctx, appID, deviceID, updateID, crashedAt))
	health, err := store.UpdateHealthByIDs(ctx, appID, []string{updateID})
	require.NoError(t, err)
	require.EqualValues(t, 1, health[updateID].RuntimeIssues)
	require.EqualValues(t, 1, health[updateID].FailedStillOn)

	recoveredAt := crashedAt.Add(time.Second)
	require.NoError(t, store.ResolveRuntimeFailure(ctx, appID, deviceID, updateID, recoveredAt))
	health, err = store.UpdateHealthByIDs(ctx, appID, []string{updateID})
	require.NoError(t, err)
	require.Zero(t, health[updateID].RuntimeIssues)
	require.Zero(t, health[updateID].FailedStillOn)

	// An offline batch may deliver an older crash after the recovery. Event
	// time, not ingestion order, keeps the device healthy.
	require.NoError(t, store.RecordRuntimeFailure(
		ctx, appID, deviceID, updateID, "late old crash", crashedAt.Add(-time.Second),
	))
	health, err = store.UpdateHealthByIDs(ctx, appID, []string{updateID})
	require.NoError(t, err)
	require.Zero(t, health[updateID].RuntimeIssues)

	// A genuinely newer crash reopens the same row and emits another immutable
	// transition; the following newer startup resolves it again.
	secondCrashAt := recoveredAt.Add(time.Second)
	require.NoError(t, store.RecordRuntimeFailure(
		ctx, appID, deviceID, updateID, "second crash", secondCrashAt,
	))
	health, err = store.UpdateHealthByIDs(ctx, appID, []string{updateID})
	require.NoError(t, err)
	require.EqualValues(t, 1, health[updateID].RuntimeIssues)
	require.NoError(t, store.ResolveRuntimeFailure(
		ctx, appID, deviceID, updateID, secondCrashAt.Add(time.Second),
	))

	rows, err := pool.Query(ctx, `
		SELECT event_type, occurred_at
		FROM device_health_outbox
		WHERE app_id = $1 AND eas_client_id = $2 AND update_id = $3
		  AND event_type IN ('failure', 'recovered')
		ORDER BY occurred_at, event_type`,
		appID, deviceID, updateID)
	require.NoError(t, err)
	defer rows.Close()
	type transition struct {
		eventType  string
		occurredAt time.Time
	}
	var transitions []transition
	for rows.Next() {
		var item transition
		require.NoError(t, rows.Scan(&item.eventType, &item.occurredAt))
		item.occurredAt = item.occurredAt.UTC()
		transitions = append(transitions, item)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []transition{
		{eventType: "failure", occurredAt: crashedAt},
		{eventType: "recovered", occurredAt: recoveredAt},
		{eventType: "failure", occurredAt: secondCrashAt},
		{eventType: "recovered", occurredAt: secondCrashAt.Add(time.Second)},
	}, transitions)

	// A startup can reach the server before a delayed older crash from another
	// batch. The standalone runtime watermark must remember it even though no
	// failure row exists yet.
	otherDeviceID := uuid.NewString()
	startedAt := crashedAt.Add(5 * time.Minute)
	require.NoError(t, store.ResolveRuntimeFailure(
		ctx, appID, otherDeviceID, updateID, startedAt,
	))
	require.NoError(t, store.RecordRuntimeFailure(
		ctx, appID, otherDeviceID, updateID, "offline old crash", startedAt.Add(-time.Second),
	))
	health, err = store.UpdateHealthByIDs(ctx, appID, []string{updateID})
	require.NoError(t, err)
	require.Zero(t, health[updateID].RuntimeIssues)
	var otherFailureRows int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM device_update_failures
		WHERE app_id = $1 AND eas_client_id = $2 AND update_id = $3`,
		appID, otherDeviceID, updateID).Scan(&otherFailureRows))
	require.Zero(t, otherFailureRows)
}

func TestRecordUpdateFailuresRejectsAnotherAppsUpdate(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	otherAppID := seedApp(t, pool)
	foreignUpdate := uuid.NewString()
	seedPublishedUpdate(t, pool, otherAppID, foreignUpdate)

	require.NoError(t, store.RecordUpdateFailures(
		context.Background(), appID, uuid.NewString(), []string{foreignUpdate},
		"forged", FailureTypeUpdate,
	))

	var rows int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM device_update_failures WHERE app_id = $1 AND update_id = $2",
		appID, foreignUpdate).Scan(&rows))
	require.Zero(t, rows)
}

func TestHealthSnapshotsKeepActiveRolloutCandidateAndControl(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	controlUUID, candidateUUID := uuid.NewString(), uuid.NewString()

	var branchID, runtimeVersionID int64
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO branches (app_id, name) VALUES ($1, 'snapshot-rollout') RETURNING id",
		appID).Scan(&branchID))
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO runtime_versions (app_id, version) VALUES ($1, 'snapshot-rollout') RETURNING id",
		appID).Scan(&runtimeVersionID))
	_, err := pool.Exec(ctx, `
		INSERT INTO updates
			(id, update_uuid, branch_id, runtime_version_id, update_type, commit_hash, platform, checked_at)
		VALUES
			(1, $1, $3, $4, 0, 'control', 'ios', CURRENT_TIMESTAMP),
			(2, $2, $3, $4, 0, 'candidate', 'ios', CURRENT_TIMESTAMP)`,
		controlUUID, candidateUUID, branchID, runtimeVersionID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		UPDATE updates
		SET rollout_percentage = 10, control_update_id = 1
		WHERE branch_id = $1 AND id = 2`,
		branchID)
	require.NoError(t, err)

	require.NoError(t, store.TouchDevice(ctx, appID, uuid.NewString(), nil, &controlUUID, DeviceInfo{}))
	require.NoError(t, store.TouchDevice(ctx, appID, uuid.NewString(), nil, &candidateUUID, DeviceInfo{}))

	readRoles := func() map[string]string {
		t.Helper()
		rows, err := store.engine.Queries.ListCurrentUpdateHealthSnapshots(ctx)
		require.NoError(t, err)
		roles := make(map[string]string)
		for _, row := range rows {
			if uuid.UUID(row.AppID.Bytes).String() == appID {
				roles[uuid.UUID(row.UpdateUuid.Bytes).String()] = row.Role
				require.Positive(t, row.DevicesOnUpdate)
			}
		}
		return roles
	}
	require.Equal(t, map[string]string{
		candidateUUID: "candidate",
		controlUUID:   "control",
	}, readRoles())

	// Once the rollout finishes the control stops being watched as a control,
	// but a device is still on it: "how many are stuck on the old one" is only
	// answerable if the series keeps being recorded, so it becomes legacy
	// rather than disappearing.
	_, err = pool.Exec(ctx, `
		UPDATE updates
		SET rollout_percentage = NULL
		WHERE branch_id = $1 AND id = 2`, branchID)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		candidateUUID: "current",
		controlUUID:   "legacy",
	}, readRoles())

	// An update nobody runs any more earns no row: the series is bounded by
	// where the fleet sits, not by everything ever published.
	_, err = pool.Exec(ctx,
		"UPDATE device_identity SET current_update_id = $1 WHERE app_id = $2 AND current_update_id = $3",
		candidateUUID, appID, controlUUID)
	require.NoError(t, err)
	roles := readRoles()
	require.NotContains(t, roles, controlUUID)
	require.Equal(t, "current", roles[candidateUUID])
}

// The two failure sources describe different events on the same (device,
// update) pair. They shared one row until failure_type joined the key, and
// whichever landed first owned the type: these two tests pin both orders.
//
// Order 1: a JS crash, then a launch rollback. The rollback must not inherit
// 'runtime_issue', because a later successful start resolves runtime rows and
// would erase durable evidence of a native rollback.
func TestRuntimeCrashThenLaunchRollbackKeepBothTypes(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()

	updateID := uuid.NewString()
	seedPublishedUpdate(t, pool, appID, updateID)
	device := uuid.NewString()
	crashedAt := time.Now().Add(-10 * time.Minute)

	require.NoError(t, store.RecordRuntimeFailure(ctx, appID, device, updateID, "js boom", crashedAt))
	require.NoError(t, store.RecordUpdateFailures(ctx, appID, device, []string{updateID}, "native boom", FailureTypeUpdate))

	health, err := store.UpdateHealthByIDs(ctx, appID, []string{updateID})
	require.NoError(t, err)
	entry := health[updateID]
	require.EqualValues(t, 1, entry.UpdateIssues, "the rollback must be typed update_issue")
	require.EqualValues(t, 1, entry.RuntimeIssues, "the JS crash must keep its own row")
	require.EqualValues(t, 1, entry.FaultyDevices, "one device, counted once")

	// A successful start resolves the runtime row and only the runtime row.
	require.NoError(t, store.ResolveRuntimeFailure(ctx, appID, device, updateID, time.Now()))
	health, err = store.UpdateHealthByIDs(ctx, appID, []string{updateID})
	require.NoError(t, err)
	entry = health[updateID]
	require.EqualValues(t, 0, entry.RuntimeIssues, "the JS crash is resolved")
	require.EqualValues(t, 1, entry.UpdateIssues, "the rollback is durable and must survive")
	require.EqualValues(t, 1, entry.FaultyDevices)
}

// Order 2: a launch rollback, then a JS crash. The crash must not be swallowed
// by the update_issue row, which no successful start can ever resolve.
func TestLaunchRollbackThenRuntimeCrashKeepBothTypes(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()

	updateID := uuid.NewString()
	seedPublishedUpdate(t, pool, appID, updateID)
	device := uuid.NewString()

	require.NoError(t, store.RecordUpdateFailures(ctx, appID, device, []string{updateID}, "native boom", FailureTypeUpdate))
	require.NoError(t, store.RecordRuntimeFailure(ctx, appID, device, updateID, "js boom", time.Now().Add(-time.Minute)))

	health, err := store.UpdateHealthByIDs(ctx, appID, []string{updateID})
	require.NoError(t, err)
	entry := health[updateID]
	require.EqualValues(t, 1, entry.RuntimeIssues, "the JS crash must be visible as a runtime issue")
	require.EqualValues(t, 1, entry.UpdateIssues)
	require.EqualValues(t, 1, entry.FaultyDevices, "still one device")

	// And it is resolvable, which it never was while it shared the rollback's row.
	require.NoError(t, store.ResolveRuntimeFailure(ctx, appID, device, updateID, time.Now()))
	health, err = store.UpdateHealthByIDs(ctx, appID, []string{updateID})
	require.NoError(t, err)
	require.EqualValues(t, 0, health[updateID].RuntimeIssues)
}

func TestUpdateHealthCounts(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	updateA, updateB := uuid.NewString(), uuid.NewString()
	seedPublishedUpdate(t, pool, appID, updateB)

	// 2 devices on A, 1 on B, 1 on the embedded bundle; B crashed on one.
	d1, d2, d3, d4 := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, store.TouchDevice(ctx, appID, d1, nil, &updateA, DeviceInfo{}))
	require.NoError(t, store.TouchDevice(ctx, appID, d2, nil, &updateA, DeviceInfo{}))
	require.NoError(t, store.TouchDevice(ctx, appID, d3, nil, &updateB, DeviceInfo{}))
	require.NoError(t, store.TouchDevice(ctx, appID, d4, nil, nil, DeviceInfo{}))
	require.NoError(t, store.RecordUpdateFailures(ctx, appID, d4, []string{updateB}, "crashed at launch", FailureTypeUpdate))

	appUUID, err := toPgUUID(appID)
	require.NoError(t, err)
	updateAUUID, err := toPgUUID(updateA)
	require.NoError(t, err)
	updateBUUID, err := toPgUUID(updateB)
	require.NoError(t, err)

	onA, err := store.engine.CountDevicesOnUpdate(ctx, pgdb.CountDevicesOnUpdateParams{AppID: appUUID, CurrentUpdateID: updateAUUID})
	require.NoError(t, err)
	require.EqualValues(t, 2, onA)
	failuresB, err := store.engine.CountUpdateFailures(ctx, pgdb.CountUpdateFailuresParams{AppID: appUUID, UpdateID: updateBUUID})
	require.NoError(t, err)
	require.EqualValues(t, 1, failuresB)

	breakdown, err := store.engine.AdoptionBreakdown(ctx, appUUID)
	require.NoError(t, err)
	require.Len(t, breakdown, 3, "A cohort, B cohort, embedded-bundle cohort")
	require.EqualValues(t, 2, breakdown[0].DeviceCount)
}

func TestUpdateHealthByIDs(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()
	updateA, updateB, updateGhost := uuid.NewString(), uuid.NewString(), uuid.NewString()
	seedPublishedUpdate(t, pool, appID, updateB)

	// 2 devices running A this month, 1 running B; B also crashed at launch
	// on d4 (rolled back, current unknown).
	d1, d2, d3, d4 := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, store.TouchDevice(ctx, appID, d1, nil, &updateA, DeviceInfo{}))
	require.NoError(t, store.TouchDevice(ctx, appID, d2, nil, &updateA, DeviceInfo{}))
	require.NoError(t, store.TouchDevice(ctx, appID, d3, nil, &updateB, DeviceInfo{}))
	require.NoError(t, store.TouchDevice(ctx, appID, d4, nil, nil, DeviceInfo{}))
	require.NoError(t, store.RecordUpdateFailures(ctx, appID, d4, []string{updateB}, "boom", FailureTypeUpdate))

	// JS crashes on B: d5 crashed and still runs it (the usual runtime_issue
	// shape), d6 crashed then moved on to A (the mismatch case the overlap
	// join must self-correct).
	d5, d6 := uuid.NewString(), uuid.NewString()
	require.NoError(t, store.TouchDevice(ctx, appID, d5, nil, &updateB, DeviceInfo{}))
	require.NoError(t, store.RecordUpdateFailures(ctx, appID, d5, []string{updateB}, "TypeError: boom", FailureTypeRuntime))
	require.NoError(t, store.TouchDevice(ctx, appID, d6, nil, &updateB, DeviceInfo{}))
	require.NoError(t, store.RecordUpdateFailures(ctx, appID, d6, []string{updateB}, "", FailureTypeRuntime))
	require.NoError(t, store.TouchDevice(ctx, appID, d6, nil, &updateA, DeviceInfo{}))

	// Adoption is the TOTAL population on the update: a device last seen a
	// month ago still runs it and still counts.
	dOld := uuid.NewString()
	require.NoError(t, store.TouchDevice(ctx, appID, dOld, nil, &updateA, DeviceInfo{}))
	_, err := pool.Exec(ctx,
		"UPDATE device_identity SET last_seen_at = date_trunc('month', CURRENT_TIMESTAMP) - INTERVAL '1 day' WHERE app_id = $1 AND eas_client_id = $2",
		appID, dOld)
	require.NoError(t, err)

	health, err := store.UpdateHealthByIDs(ctx, appID, []string{updateA, updateB, updateGhost, "not-a-uuid"})
	require.NoError(t, err)
	require.EqualValues(t, 4, health[updateA].DevicesOnUpdate, "d1, d2, dOld and the moved-on d6")
	require.Zero(t, health[updateA].UpdateIssues)
	require.Zero(t, health[updateA].RuntimeIssues)
	require.EqualValues(t, 2, health[updateB].DevicesOnUpdate, "d3 and the crashed-but-running d5")
	require.EqualValues(t, 1, health[updateB].UpdateIssues)
	require.EqualValues(t, 2, health[updateB].RuntimeIssues)
	require.EqualValues(t, 1, health[updateB].FailedStillOn, "d5 overlaps; d4 rolled back, d6 moved on")
	// An update nothing attempted has no entry: zero-valued on read.
	require.Zero(t, health[updateGhost])
}

// storedAppVersion reads the column directly: nothing on the read API exposes
// it, because the only consumer is the outbox resolving an event's dimensions.
func storedAppVersion(t *testing.T, pool *pgxpool.Pool, appID, deviceID string) *string {
	t.Helper()
	var appVersion *string
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT app_version FROM device_identity WHERE app_id = $1 AND eas_client_id = $2",
		appID, deviceID).Scan(&appVersion))
	return appVersion
}

// Hardware and store version reach the registry through telemetry only, so the
// columns have to survive every manifest poll that follows, and an upgrade of
// either has to land.
func TestTouchDeviceHardwareCoalesce(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()

	deviceID := uuid.NewString()
	reported := DeviceInfo{Model: "iPhone18,2", OSName: "iOS", OSVersion: "26.1", AppVersion: "1.4.0"}
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, nil, reported))

	registered, err := store.GetDevice(ctx, appID, deviceID)
	require.NoError(t, err)
	require.NotNil(t, registered.DeviceModel)
	require.Equal(t, "iPhone18,2", *registered.DeviceModel)
	require.Equal(t, "iOS", *registered.OSName)
	require.Equal(t, "26.1", *registered.OSVersion)
	require.Equal(t, "1.4.0", *storedAppVersion(t, pool, appID, deviceID))

	// A manifest poll knows no hardware and must leave it untouched.
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, nil, DeviceInfo{}))
	kept, err := store.GetDevice(ctx, appID, deviceID)
	require.NoError(t, err)
	require.NotNil(t, kept.DeviceModel)
	require.Equal(t, "iPhone18,2", *kept.DeviceModel)
	require.Equal(t, "26.1", *kept.OSVersion)
	require.Equal(t, "1.4.0", *storedAppVersion(t, pool, appID, deviceID))

	// A real OS upgrade does land, and so does a store release.
	upgraded := reported
	upgraded.OSVersion = "26.2"
	upgraded.AppVersion = "1.5.0"
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, nil, upgraded))
	after, err := store.GetDevice(ctx, appID, deviceID)
	require.NoError(t, err)
	require.Equal(t, "26.2", *after.OSVersion)
	require.Equal(t, "1.5.0", *storedAppVersion(t, pool, appID, deviceID))
}

// The same must hold on the registration arm, where the row does not exist yet
// and the upsert's ON CONFLICT is what writes.
func TestRegisterDeviceKeepsHardwareOnConflict(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()

	deviceID := uuid.NewString()
	// First contact ever carries no hardware (a manifest poll), so the row is
	// created empty and the first telemetry batch has to fill it.
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, nil, DeviceInfo{}))
	require.NoError(t, store.TouchDevice(ctx, appID, deviceID, nil, nil, DeviceInfo{
		Model: "SM-A536B", OSName: "Android", OSVersion: "14",
	}))

	device, err := store.GetDevice(ctx, appID, deviceID)
	require.NoError(t, err)
	require.NotNil(t, device.DeviceModel)
	require.Equal(t, "SM-A536B", *device.DeviceModel)
	require.Equal(t, "Android", *device.OSName)
}

// The inventory joins the release dimensions from the update each device runs,
// rather than storing them: an update never changes branch, so there is
// nothing to keep in sync. The join must be LEFT, or every device on the
// embedded bundle would vanish from the unfiltered list.
// A publish produces one update per platform, and a device only ever stores
// the update it runs. "Devices on this update group" therefore has to reach
// them through their update, which is what the join is for.
func TestListDevicesFiltersOnUpdateGroup(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()

	iosUpdate, androidUpdate, otherUpdate := uuid.NewString(), uuid.NewString(), uuid.NewString()
	group, otherGroup := uuid.NewString(), uuid.NewString()
	seedPublishedUpdate(t, pool, appID, iosUpdate)
	seedGroupedUpdate(t, pool, appID, androidUpdate, group, 2)
	seedGroupedUpdate(t, pool, appID, otherUpdate, otherGroup, 3)
	_, err := pool.Exec(ctx, "UPDATE updates SET publish_group = $1 WHERE update_uuid = $2", group, iosUpdate)
	require.NoError(t, err)

	onIOS, onAndroid, elsewhere := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, store.TouchDevice(ctx, appID, onIOS, nil, &iosUpdate, DeviceInfo{}))
	require.NoError(t, store.TouchDevice(ctx, appID, onAndroid, nil, &androidUpdate, DeviceInfo{}))
	require.NoError(t, store.TouchDevice(ctx, appID, elsewhere, nil, &otherUpdate, DeviceInfo{}))

	// Both halves of the publish, and nothing from the other one.
	devices, _, err := store.ListDevices(ctx, appID, DeviceQuery{UpdateGroupIDs: []string{group}}, 10, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{onIOS, onAndroid}, clientIDs(devices))

	// Several groups compare populations, like every other dimension.
	both, _, err := store.ListDevices(ctx, appID, DeviceQuery{
		UpdateGroupIDs: []string{group, otherGroup},
	}, 10, nil)
	require.NoError(t, err)
	require.Len(t, both, 3)
}

func clientIDs(devices []Device) []string {
	out := make([]string, 0, len(devices))
	for _, device := range devices {
		out = append(out, device.EASClientID)
	}
	return out
}

func TestListDevicesReportsReleaseDimensions(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()

	updateID := uuid.NewString()
	seedPublishedUpdate(t, pool, appID, updateID)
	branch := "health-" + updateID[:8]

	onUpdate := uuid.NewString()
	require.NoError(t, store.TouchDevice(ctx, appID, onUpdate, nil, &updateID, DeviceInfo{
		Model: "iPhone18,2", OSName: "iOS", OSVersion: "26.1",
	}))
	// A device on the embedded bundle reports an id no published update
	// matches, so the resolution stores nothing.
	embedded := uuid.NewString()
	embeddedUpdate := uuid.NewString()
	require.NoError(t, store.TouchDevice(ctx, appID, embedded, nil, &embeddedUpdate, DeviceInfo{}))

	all, _, err := store.ListDevices(ctx, appID, DeviceQuery{}, 10, nil)
	require.NoError(t, err)
	require.Len(t, all, 2, "an unknown update must not drop a device from the inventory")

	byBranch, _, err := store.ListDevices(ctx, appID, DeviceQuery{Branches: []string{branch}}, 10, nil)
	require.NoError(t, err)
	require.Len(t, byBranch, 1)
	require.Equal(t, onUpdate, byBranch[0].EASClientID)
	require.NotNil(t, byBranch[0].Branch)
	require.Equal(t, branch, *byBranch[0].Branch)
	require.NotNil(t, byBranch[0].Platform)
	require.Equal(t, "ios", *byBranch[0].Platform)
	require.NotNil(t, byBranch[0].CurrentUpdateID)
	require.Equal(t, updateID, *byBranch[0].CurrentUpdateID)

	// Hardware filters hit the registry columns directly.
	byModel, _, err := store.ListDevices(ctx, appID, DeviceQuery{DeviceModels: []string{"iPhone18,2"}}, 10, nil)
	require.NoError(t, err)
	require.Len(t, byModel, 1)
	require.Equal(t, onUpdate, byModel[0].EASClientID)

	byOS, _, err := store.ListDevices(ctx, appID, DeviceQuery{OSNames: []string{"iOS"}, OSVersions: []string{"26.1"}}, 10, nil)
	require.NoError(t, err)
	require.Len(t, byOS, 1)

	// The embedded-bundle device is listed, with no release dimensions to
	// report. Looked up by id: the page is ordered by last_seen, not by
	// insertion.
	var embeddedRow *Device
	for i := range all {
		if all[i].EASClientID == embedded {
			embeddedRow = &all[i]
		}
	}
	require.NotNil(t, embeddedRow)
	require.Nil(t, embeddedRow.Branch)
	require.Nil(t, embeddedRow.RuntimeVersion)
	require.Nil(t, embeddedRow.Platform)
}

// current_update_id arrives on the unauthenticated wire, so a device of one app
// can name an update of another. The release dimensions are resolved once at
// check-in, scoped to the app, which is where that claim has to be refused: a
// leak here would put another app's branch, runtime and platform in this app's
// inventory, and make its releases filterable from the outside.
func TestDeviceReleaseDimensionsRefuseAnotherAppsUpdate(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	otherAppID := seedApp(t, pool)
	ctx := context.Background()

	foreignUpdate := uuid.NewString()
	seedPublishedUpdate(t, pool, otherAppID, foreignUpdate)
	foreignBranch := "health-" + foreignUpdate[:8]

	claimant := uuid.NewString()
	require.NoError(t, store.TouchDevice(ctx, appID, claimant, nil, &foreignUpdate, DeviceInfo{}))

	// The device is still registered: an unattributable update is not a reason
	// to lose the install.
	all, _, err := store.ListDevices(ctx, appID, DeviceQuery{}, 10, nil)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, claimant, all[0].EASClientID)
	require.Nil(t, all[0].Branch, "another app's branch must not reach this inventory")
	require.Nil(t, all[0].RuntimeVersion)
	require.Nil(t, all[0].Platform)

	// And the other app's release is not filterable from here.
	byForeign, _, err := store.ListDevices(ctx, appID, DeviceQuery{Branches: []string{foreignBranch}}, 10, nil)
	require.NoError(t, err)
	require.Empty(t, byForeign)

	count, err := store.CountOnlineDevices(ctx, appID, time.Now().Add(-time.Hour), DeviceQuery{
		Branches: []string{foreignBranch},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, count)
}

// The online count sits next to filtered figures, so it narrows on the same
// dimensions as the inventory. Two things it must not do: drop a device the
// registry knows nothing more about, and treat "no release filter" as "every
// update of the app", which is the same thing said the other way round.
func TestCountOnlineDevicesFilters(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()

	first, second := uuid.NewString(), uuid.NewString()
	group := uuid.NewString()
	seedPublishedUpdate(t, pool, appID, first)
	seedGroupedUpdate(t, pool, appID, second, group, 2)
	firstBranch, secondBranch := "health-"+first[:8], "group-"+second[:8]

	onFirst, onSecond, embedded := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, store.TouchDevice(ctx, appID, onFirst, nil, &first, DeviceInfo{Model: "iPhone18,2"}))
	require.NoError(t, store.TouchDevice(ctx, appID, onSecond, nil, &second, DeviceInfo{Model: "SM-A546B"}))
	// Reports an update no publish matches, so it joins to nothing.
	unknownUpdate := uuid.NewString()
	require.NoError(t, store.TouchDevice(ctx, appID, embedded, nil, &unknownUpdate, DeviceInfo{}))

	since := time.Now().UTC().Add(-DefaultOnlineWindow)
	all, err := store.CountOnlineDevices(ctx, appID, since, DeviceQuery{})
	require.NoError(t, err)
	require.EqualValues(t, 3, all, "a device on an unknown update must still count as online")

	byBranch, err := store.CountOnlineDevices(ctx, appID, since, DeviceQuery{Branches: []string{firstBranch}})
	require.NoError(t, err)
	require.EqualValues(t, 1, byBranch)

	bothBranches, err := store.CountOnlineDevices(ctx, appID, since, DeviceQuery{
		Branches: []string{firstBranch, secondBranch},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, bothBranches)

	// The other arms of the same subquery: seedPublishedUpdate is ios, the
	// grouped one android.
	byPlatform, err := store.CountOnlineDevices(ctx, appID, since, DeviceQuery{Platforms: []string{"android"}})
	require.NoError(t, err)
	require.EqualValues(t, 1, byPlatform)

	byGroup, err := store.CountOnlineDevices(ctx, appID, since, DeviceQuery{UpdateGroupIDs: []string{group}})
	require.NoError(t, err)
	require.EqualValues(t, 1, byGroup)

	// A branch nobody runs is zero, not the whole fleet: the release filter
	// resolving to nothing must not read as "no release filter".
	none, err := store.CountOnlineDevices(ctx, appID, since, DeviceQuery{Branches: []string{"no-such-branch"}})
	require.NoError(t, err)
	require.EqualValues(t, 0, none)

	// Filters combine, they do not widen: this device is on the first branch,
	// so asking for it on the second one's model is zero.
	crossed, err := store.CountOnlineDevices(ctx, appID, since, DeviceQuery{
		Branches:     []string{firstBranch},
		DeviceModels: []string{"SM-A546B"},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, crossed)

	byModel, err := store.CountOnlineDevices(ctx, appID, since, DeviceQuery{DeviceModels: []string{"SM-A546B"}})
	require.NoError(t, err)
	require.EqualValues(t, 1, byModel)

	// The window still cuts first: a device that went quiet is out whatever the
	// filters say.
	_, err = pool.Exec(ctx,
		"UPDATE device_identity SET last_seen_at = now() - interval '2 hours' WHERE app_id = $1 AND eas_client_id = $2",
		appID, onFirst)
	require.NoError(t, err)
	stale, err := store.CountOnlineDevices(ctx, appID, since, DeviceQuery{Branches: []string{firstBranch}})
	require.NoError(t, err)
	require.EqualValues(t, 0, stale)
}

// Several values per filter is what makes a comparison possible: two branches
// side by side rather than one query each.
func TestListDevicesAcceptsSeveralValues(t *testing.T) {
	store, pool := setupIdentityStore(t)
	appID := seedApp(t, pool)
	ctx := context.Background()

	first := uuid.NewString()
	second := uuid.NewString()
	seedPublishedUpdate(t, pool, appID, first)
	seedPublishedUpdate(t, pool, appID, second)

	onFirst := uuid.NewString()
	onSecond := uuid.NewString()
	require.NoError(t, store.TouchDevice(ctx, appID, onFirst, nil, &first, DeviceInfo{Model: "iPhone18,2"}))
	require.NoError(t, store.TouchDevice(ctx, appID, onSecond, nil, &second, DeviceInfo{Model: "SM-A546B"}))

	both, _, err := store.ListDevices(ctx, appID, DeviceQuery{
		Branches: []string{"health-" + first[:8], "health-" + second[:8]},
	}, 10, nil)
	require.NoError(t, err)
	require.Len(t, both, 2)

	// A model identifier carries a comma, and the array predicate treats it as
	// one value rather than two.
	models, _, err := store.ListDevices(ctx, appID, DeviceQuery{
		DeviceModels: []string{"iPhone18,2", "SM-A546B"},
	}, 10, nil)
	require.NoError(t, err)
	require.Len(t, models, 2)

	single, _, err := store.ListDevices(ctx, appID, DeviceQuery{
		DeviceModels: []string{"iPhone18,2"},
	}, 10, nil)
	require.NoError(t, err)
	require.Len(t, single, 1)
	require.Equal(t, onFirst, single[0].EASClientID)
}
