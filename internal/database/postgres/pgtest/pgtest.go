// Package pgtest serializes the test packages that share TEST_DATABASE_URL.
//
// go test ./... runs packages in parallel, and the Postgres-backed store tests
// (internal/store, ee/rbac, ee/sso) all point at the same database. Their
// cleanups are deliberately wholesale (DELETE FROM users) and their assertions
// global (admin counts), so two packages running at once wipe each other's
// rows mid-test. Every package that touches TEST_DATABASE_URL must declare:
//
//	func TestMain(m *testing.M) { os.Exit(pgtest.RunSerialized(m)) }
package pgtest

import (
	"context"
	"database/sql"
	"log"
	"os"
	"testing"
	"time"
	"xprem/internal/database/postgres"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Upper bound on waiting for the other test packages to finish.
const packageLockTimeout = 5 * time.Minute

// A blocking pg_advisory_lock query remains an active transaction while it
// waits. CREATE INDEX CONCURRENTLY in the lock holder then waits for that
// transaction, creating a cycle. Polling the non-blocking variant lets each
// failed attempt finish before the migrator checks for active transactions.
const packageLockRetryInterval = 100 * time.Millisecond

// RunSerialized runs the package's tests under a Postgres advisory lock shared
// by every test package that uses TEST_DATABASE_URL, so at most one of them
// touches the database at a time. Without the env var it just runs the tests
// (they skip or fail on their own).
func RunSerialized(m *testing.M) int {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		return m.Run()
	}

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatalf("pgtest: failed to open connection for the package lock: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), packageLockTimeout)
	defer cancel()

	// The lock is session-scoped, so it must live on a dedicated connection
	// pinned for the whole run; Postgres releases it when the process exits.
	conn, err := db.Conn(ctx)
	if err != nil {
		log.Fatalf("pgtest: failed to acquire connection for the package lock: %v", err)
	}
	defer conn.Close()
	if err := waitForPackageLock(ctx, packageLockRetryInterval, func(ctx context.Context) (bool, error) {
		var acquired bool
		err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", postgres.TestPackageLockID).Scan(&acquired)
		return acquired, err
	}); err != nil {
		log.Fatalf("pgtest: failed to acquire the package lock: %v", err)
	}

	return m.Run()
}

func waitForPackageLock(
	ctx context.Context,
	retryInterval time.Duration,
	tryLock func(context.Context) (bool, error),
) error {
	retry := time.NewTicker(retryInterval)
	defer retry.Stop()

	for {
		acquired, err := tryLock(ctx)
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-retry.C:
		}
	}
}
