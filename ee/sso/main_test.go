// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package sso

import (
	"os"
	"testing"

	"xprem/internal/database/postgres/pgtest"
)

// The Postgres-backed tests in this package share TEST_DATABASE_URL with
// internal/store and ee/rbac; pgtest serializes the packages so their wholesale
// cleanups cannot wipe each other's rows.
func TestMain(m *testing.M) {
	os.Exit(pgtest.RunSerialized(m))
}
