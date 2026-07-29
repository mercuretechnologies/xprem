package migrations

import "xprem/internal/database"

// dbEngine is the shared database engine used by Go-based migrations. It is set
// once from wire.go (via SetEngine) before goose runs, and read by any
// migration that needs to execute queries against the live engine (e.g. the
// infra→DB data migration).
//
// It lives here — rather than inside a dated migration file — so the injection
// point is discoverable and every migration file stays self-contained. New Go
// migrations can reference dbEngine without hunting for where it's declared.
var dbEngine *database.Engine

// SetEngine injects the shared database engine. Called from wire.go before the
// migrations run.
func SetEngine(e *database.Engine) {
	dbEngine = e
}
