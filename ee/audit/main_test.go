// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package audit

import (
	"os"
	"testing"

	"xprem/internal/database/postgres/pgtest"
)

// The Postgres-backed tests share TEST_DATABASE_URL with other packages;
// pgtest serializes them so migrations and test transactions cannot conflict.
func TestMain(m *testing.M) {
	os.Exit(pgtest.RunSerialized(m))
}
