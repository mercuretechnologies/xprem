package clickhouse

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"log"
	"xprem/internal/database/postgres"

	_ "github.com/ClickHouse/clickhouse-go/v2" // registers the "clickhouse" database/sql driver
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var embedMigrations embed.FS

// RunDBMigrations applies the ClickHouse schema. ClickHouse has no advisory
// locks, so cross-replica serialization borrows a Postgres one (see
// postgres.AcquireMigrationLock): the control plane is always configured
// when ClickHouse is, since Observe requires DB mode. Without it,
// concurrently booting replicas would race inside goose's version
// bookkeeping, and any future data-moving migration (a backfill INSERT ...
// SELECT) would apply twice.
//
// It uses goose's Provider API rather than the global goose.SetBaseFS/
// SetDialect the Postgres path uses: those globals are process-wide, and
// clobbering them here would break any later (or test-parallel) Postgres
// migration run.
func RunDBMigrations(dsn string, controlPlaneDBURL string) {
	release := postgres.AcquireMigrationLock(controlPlaneDBURL, postgres.ClickHouseMigrationLockID)
	defer release()

	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		log.Fatalf("❌ [CLICKHOUSE] Failed to open SQL connection for schema migrations: %v", err)
	}
	defer db.Close()

	migrationsFS, err := fs.Sub(embedMigrations, "migrations")
	if err != nil {
		log.Fatalf("❌ [CLICKHOUSE] Failed to open embedded migrations: %v", err)
	}

	// WithAllowOutofOrder for the same reason the Postgres runner passes
	// WithAllowMissing: parallel PRs merge out of timestamp order.
	// WithDisableGlobalRegistry because the Postgres schema ships Go
	// migrations (the seed-admin-user one) that init()-register themselves
	// in goose's global registry; without this the provider would try to
	// run them against ClickHouse.
	provider, err := goose.NewProvider(goose.DialectClickHouse, db, migrationsFS,
		goose.WithAllowOutofOrder(true),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		log.Fatalf("❌ [CLICKHOUSE] Failed to create migration provider: %v", err)
	}

	log.Println("🔧 [CLICKHOUSE] Checking and running ClickHouse schema migrations...")

	if _, err := provider.Up(context.Background()); err != nil {
		log.Fatalf("🚨 [CLICKHOUSE] ClickHouse migration execution failed: %v", err)
	}

	log.Println("🎉 [CLICKHOUSE] ClickHouse schema up to date!")
}
