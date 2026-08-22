// Integration tests for the progressive-rollout queries. They need a real Postgres
// and skip unless TEST_DATABASE_URL is set, e.g.:
//
//	docker run -d --name eoo-pg -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:16-alpine
//	TEST_DATABASE_URL="postgres://postgres:test@localhost:55432/postgres?sslmode=disable" go test ./internal/store/
//
// The package is store_test to avoid an import cycle (store -> database/postgres -> migrations -> store).
package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"xprem/internal/bucket"
	"xprem/internal/database"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/store"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)
