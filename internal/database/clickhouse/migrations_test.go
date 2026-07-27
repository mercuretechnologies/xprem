package clickhouse

import (
	"context"
	"expo-open-ota/internal/database/postgres/pgtest"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// The migration lock lives on TEST_DATABASE_URL, so this package follows the
// pgtest serialization convention like every package touching it.
func TestMain(m *testing.M) { os.Exit(pgtest.RunSerialized(m)) }

// Needs real servers: set TEST_CLICKHOUSE_URL (e.g. the docker-compose
// service, clickhouse://default:secret@localhost:9000/expo_ota_dev) and
// TEST_DATABASE_URL (the advisory lock serializing migrators is a Postgres
// one) to run. Applies the embedded migrations twice (a replica booting
// after the leader must no-op), checks the fact tables exist and the
// Postgres-migrated Source B tables are gone.
func TestRunDBMigrations(t *testing.T) {
	dsn, pgURL := requireLiveStores(t)

	RunDBMigrations(dsn, pgURL)
	RunDBMigrations(dsn, pgURL)

	ctx := context.Background()
	engine, err := NewClickHouseEngine(ctx, dsn)
	require.NoError(t, err)
	defer engine.Close()

	for _, table := range []string{
		"observe_metrics",
		"observe_logs",
		"device_health_events",
		"update_health_snapshots",
	} {
		var exists uint64
		require.NoError(t, engine.Conn.QueryRow(ctx,
			"SELECT count() FROM system.tables WHERE database = currentDatabase() AND name = ?", table,
		).Scan(&exists), table)
		require.EqualValues(t, 1, exists, "table %s should exist", table)
	}
	// The Source B tables moved to Postgres (device_identity.current_update_id
	// + device_update_failures); the drop migration must have removed them.
	for _, table := range []string{"device_current_update", "update_crashes"} {
		var exists uint64
		require.NoError(t, engine.Conn.QueryRow(ctx,
			"SELECT count() FROM system.tables WHERE database = currentDatabase() AND name = ?", table,
		).Scan(&exists), table)
		require.EqualValues(t, 0, exists, "table %s should be dropped", table)
	}
}

// The lock contract: N replicas booting at once on a fresh database must
// yield exactly one applied migration. Each AcquireMigrationLock opens its
// own connection, so even in-process these are distinct Postgres sessions
// genuinely contending on the advisory lock. Without it, every migrator
// would see "nothing applied" and record the goose version once each (and a
// future data-moving migration would apply N times). RunDBMigrations
// log.Fatalf's on migration errors, so a racing failure kills the test
// binary loudly.
func TestConcurrentMigratorsApplyOnce(t *testing.T) {
	dsn, pgURL := requireLiveStores(t)

	ctx := context.Background()
	engine, err := NewClickHouseEngine(ctx, dsn)
	require.NoError(t, err)
	defer engine.Close()

	// Back to a virgin schema so "first ever apply" is what races. Wholesale
	// resets on the shared test database are house style (see pgtest).
	for _, table := range []string{
		"goose_db_version",
		"observe_metrics",
		"observe_logs",
		"device_health_events",
		"update_health_snapshots",
	} {
		require.NoError(t, engine.Conn.Exec(ctx, "DROP TABLE IF EXISTS "+table))
	}

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			RunDBMigrations(dsn, pgURL)
		}()
	}
	wg.Wait()

	var versionRows uint64
	require.NoError(t, engine.Conn.QueryRow(ctx,
		"SELECT count() FROM goose_db_version WHERE version_id = 20260727160000 AND is_applied = 1",
	).Scan(&versionRows))
	require.EqualValues(t, 1, versionRows, "migration must be recorded exactly once")
}

// requireLiveStores hands back the two store URLs, or stops the test. It SKIPS
// on a developer machine and FAILS in CI: a store test that skips is a green
// job having exercised none of the schema and none of the SQL it exists to
// cover, which is exactly how a renamed column or a broken migration ships.
// Same guard the identity store tests carry.
func requireLiveStores(t *testing.T) (clickhouseURL, postgresURL string) {
	t.Helper()
	clickhouseURL, postgresURL = os.Getenv("TEST_CLICKHOUSE_URL"), os.Getenv("TEST_DATABASE_URL")
	if clickhouseURL == "" || postgresURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_CLICKHOUSE_URL and TEST_DATABASE_URL must both be set in CI: these tests cover schema and queries no unit test can reach")
		}
		t.Skip("TEST_CLICKHOUSE_URL and TEST_DATABASE_URL not both set; start a Postgres and a ClickHouse and set them to run the store tests")
	}
	return clickhouseURL, postgresURL
}
