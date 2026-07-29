package postgres

import (
	"context"
	"database/sql"
	"embed"
	"log"
	"time"
	_ "xprem/internal/database/postgres/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*
var embedMigrations embed.FS

// Arbitrary app-wide id for the Postgres advisory lock that serializes migrators.
const migrationAdvisoryLockID = 823672941

// Upper bound on waiting for the advisory lock: long enough for a slow leader
// to finish its migrations, short enough that a stuck lock surfaces as a crash
// instead of a silent hang.
const migrationLockTimeout = 5 * time.Minute

// AcquireMigrationLock serializes migrators racing on the same lock id:
// parallel test packages sharing one TEST_DATABASE_URL, or multiple server
// replicas booting simultaneously. Without a lock they race inside goose
// (duplicate CREATE TABLE hits pg_type_typname_nsp_index) and the loser dies
// on Fatalf; with it the first applies, the rest wait then no-op. Advisory
// locks are session-scoped, so the lock lives on a dedicated connection that
// release closes. The ClickHouse migration runner borrows this with its own
// lock id: ClickHouse has no advisory locks, and the Postgres control plane
// is always configured when ClickHouse is.
func AcquireMigrationLock(dbURL string, lockID int64) (release func()) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatalf("❌ [DATABASE] Failed to open SQL connection for migration lock: %v", err)
	}
	lockCtx, cancel := context.WithTimeout(context.Background(), migrationLockTimeout)
	conn, err := db.Conn(lockCtx)
	if err != nil {
		log.Fatalf("❌ [DATABASE] Failed to acquire connection for migration lock: %v", err)
	}
	if _, err := conn.ExecContext(lockCtx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		log.Fatalf("❌ [DATABASE] Failed to acquire migration advisory lock: %v", err)
	}
	return func() {
		// Background, not lockCtx: the unlock runs after the migrations,
		// possibly past the timeout. A failed unlock is harmless anyway,
		// closing the connection releases the lock.
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", lockID)
		_ = conn.Close()
		_ = db.Close()
		cancel()
	}
}

func RunDBMigrations(dbURL string) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatalf("❌ [DATABASE] Failed to open SQL connection for schema migrations: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(embedMigrations)

	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatalf("❌ [DATABASE] Failed to set goose dialect: %v", err)
	}

	log.Println("🔧 [DATABASE] Checking and running PostgreSQL schema migrations...")

	release := AcquireMigrationLock(dbURL, migrationAdvisoryLockID)
	defer release()

	// WithAllowMissing applies migrations whose version is lower than the one already
	// recorded in the database. Parallel PRs get merged out of timestamp order, so a
	// deployment can pick up a migration that predates one it already ran. Migrations
	// here are independent of each other, so applying them out of order is safe.
	if err := goose.Up(db, "migrations", goose.WithAllowMissing()); err != nil {
		log.Fatalf("🚨 [DATABASE] PostgreSQL migration execution failed: %v", err)
	}

	log.Println("🎉 [DATABASE] PostgreSQL schema up to date!")
}
