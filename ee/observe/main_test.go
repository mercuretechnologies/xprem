// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

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
