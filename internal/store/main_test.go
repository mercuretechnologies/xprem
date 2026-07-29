package store_test

import (
	"os"
	"testing"

	"xprem/internal/database/postgres/pgtest"
)

// The Postgres-backed tests in this package share TEST_DATABASE_URL with
// ee/rbac and ee/sso; pgtest serializes the packages so their wholesale
// cleanups cannot wipe each other's rows.
func TestMain(m *testing.M) {
	os.Exit(pgtest.RunSerialized(m))
}
