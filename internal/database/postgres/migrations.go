package postgres

import (
	"context"
	"database/sql"
	"embed"
	"log"
	_ "xprem/internal/database/postgres/migrations"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*
var embedMigrations embed.FS

// AcquireMigrationLock serializes migrators racing on the same lock id:
// parallel test packages sharing one TEST_DATABASE_URL, or multiple server
// replicas booting simultaneously — the first applies, the rest wait then
// no-op. It exits the process when the lock cannot be taken; use
// AcquireAdvisoryLock to handle the error instead.
func AcquireMigrationLock(dbURL string, lockID int64) func() {
	connConfig, err := pgx.ParseConfig(dbURL)
	if err != nil {
		log.Fatalf("❌ [DATABASE] Failed to parse the database URL for the migration lock: %v", err)
	}
	release, err := AcquireAdvisoryLock(context.Background(), connConfig, lockID, "migration")
	if err != nil {
		log.Fatalf("❌ [DATABASE] %v", err)
	}
	return release
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

	release := AcquireMigrationLock(dbURL, MigrationLockID)
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
