// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"xprem/internal/database"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/repository"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type postgresFixture struct {
	pool    *pgxpool.Pool
	indexes *PostgresIndexRepository
	updates *repository.PostgresUpdateRepository
	appId   string
}

func newPostgresFixture(t *testing.T) *postgresFixture {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI: these tests cover SQL that the in-memory fakes cannot reach")
		}
		t.Skip("TEST_DATABASE_URL not set; start a Postgres and set it to run the sourcemap index store tests")
	}
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(dbURL)

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	engine := &database.Engine{Queries: pgdb.New(pool), DB: pool}
	ctx := context.Background()
	fixture := &postgresFixture{
		pool:    pool,
		indexes: NewPostgresIndexRepository(engine),
		updates: repository.NewPostgresUpdateRepository(engine),
		appId:   uuid.NewString(),
	}
	_, err = pool.Exec(ctx, "INSERT INTO apps (id, name) VALUES ($1, $2)", fixture.appId, "sourcemap-index-test")
	require.NoError(t, err)
	branches := repository.NewPostgresBranchRepository(engine)
	_, err = branches.InsertBranch(ctx, fixture.appId, "main")
	require.NoError(t, err)
	_, err = branches.CreateRuntimeVersion(ctx, fixture.appId, "1")
	require.NoError(t, err)
	return fixture
}

func (f *postgresFixture) createUpdate(t *testing.T, id int64) types.Update {
	t.Helper()
	created, err := f.updates.CreateUpdate(context.Background(), f.appId, id, "main", "1", "ios", "abc123", "", nil)
	require.NoError(t, err)
	return *created
}

func TestIndexRecordLifecyclePostgres(t *testing.T) {
	f := newPostgresFixture(t)
	ctx := context.Background()
	update := f.createUpdate(t, 100)

	sourcemap, err := f.indexes.GetUpdateSourcemap(ctx, f.appId, "main", "1", update.UpdateId)
	require.NoError(t, err)
	require.NotNil(t, sourcemap)
	assert.Nil(t, sourcemap.Hash, "published without a map")
	assert.Nil(t, sourcemap.Index, "no job recorded yet")
	require.NoError(t, f.updates.StoreUpdateSourcemapHash(ctx, update, testHash))

	require.NoError(t, f.indexes.MarkPending(ctx, update, testHash))
	require.NoError(t, f.indexes.MarkRunning(ctx, update, testHash, 1))
	segments, size := 36, int64(1234)
	require.NoError(t, f.indexes.Finish(ctx, update, types.SourcemapIndexStored, "", &segments, &size))

	sourcemap, err = f.indexes.GetUpdateSourcemap(ctx, f.appId, "main", "1", update.UpdateId)
	require.NoError(t, err)
	require.NotNil(t, sourcemap.Hash)
	assert.Equal(t, testHash, *sourcemap.Hash)
	record := sourcemap.Index
	require.NotNil(t, record)
	assert.Equal(t, testHash, record.Hash)
	assert.Equal(t, types.SourcemapIndexStored, record.Status)
	assert.Equal(t, 1, record.Attempts)
	require.NotNil(t, record.Segments)
	assert.Equal(t, 36, *record.Segments)
	require.NotNil(t, record.IndexSize)
	assert.EqualValues(t, 1234, *record.IndexSize)

	// A reindex resets the row, keeping it unique per update.
	require.NoError(t, f.indexes.MarkPending(ctx, update, testHash))
	sourcemap, err = f.indexes.GetUpdateSourcemap(ctx, f.appId, "main", "1", update.UpdateId)
	require.NoError(t, err)
	record = sourcemap.Index
	assert.Equal(t, types.SourcemapIndexPending, record.Status)
	assert.Equal(t, 0, record.Attempts)
	assert.Nil(t, record.Segments)

	unrecorded := f.createUpdate(t, 101)
	require.NoError(t, f.updates.StoreUpdateSourcemapHash(ctx, unrecorded, testHash))
	require.NoError(t, f.indexes.MarkRunning(ctx, unrecorded, testHash, 1))
	sourcemap, err = f.indexes.GetUpdateSourcemap(ctx, f.appId, "main", "1", unrecorded.UpdateId)
	require.NoError(t, err)
	require.NotNil(t, sourcemap.Index)
	assert.Equal(t, types.SourcemapIndexRunning, sourcemap.Index.Status)
	assert.Equal(t, 1, sourcemap.Index.Attempts)
}

func TestIndexRecordRefusesAForeignBranchPostgres(t *testing.T) {
	f := newPostgresFixture(t)
	ctx := context.Background()
	f.createUpdate(t, 100)
	foreign := types.Update{AppId: uuid.NewString(), Branch: "main", RuntimeVersion: "1", UpdateId: "100"}

	assert.Error(t, f.indexes.MarkPending(ctx, foreign, testHash))
	sourcemap, err := f.indexes.GetUpdateSourcemap(ctx, foreign.AppId, "main", "1", "100")
	require.NoError(t, err)
	assert.Nil(t, sourcemap, "unknown update")
}

// Deleting the update takes its index record along.
func TestIndexRecordFollowsTheUpdatePostgres(t *testing.T) {
	f := newPostgresFixture(t)
	ctx := context.Background()
	update := f.createUpdate(t, 100)
	require.NoError(t, f.indexes.MarkPending(ctx, update, testHash))

	_, err := f.pool.Exec(ctx, "DELETE FROM updates u USING branches b WHERE u.branch_id = b.id AND b.app_id = $1 AND u.id = 100", f.appId)
	require.NoError(t, err)
	var count int
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM sourcemap_indexes si JOIN branches b ON b.id = si.branch_id WHERE b.app_id = $1", f.appId).Scan(&count))
	assert.Equal(t, 0, count)
}

func TestUpdateSourcemapUUIDCarriesRepairIdentityForUncheckedUpdate(t *testing.T) {
	f := newPostgresFixture(t)
	ctx := context.Background()
	update := f.createUpdate(t, 100)
	require.NoError(t, f.updates.StoreUpdateSourcemapHash(ctx, update, testHash))
	updateUUID := uuid.NewString()
	_, err := f.pool.Exec(ctx, `UPDATE updates u SET update_uuid=$1, checked_at=NULL
		FROM branches b WHERE b.id=u.branch_id AND b.app_id=$2 AND u.id=100`, updateUUID, f.appId)
	require.NoError(t, err)
	require.NoError(t, f.indexes.MarkPending(ctx, update, testHash))
	require.NoError(t, f.indexes.Finish(ctx, update, types.SourcemapIndexStored, "", nil, nil))

	sourcemap, err := f.indexes.GetUpdateSourcemapByUUID(ctx, f.appId, updateUUID)
	require.NoError(t, err)
	require.NotNil(t, sourcemap, "symbolication can resolve existing updates before checked_at is populated")
	require.Equal(t, testHash, *sourcemap.Hash)
	require.Equal(t, types.SourcemapIndexStored, sourcemap.Index.Status)
	assert.Equal(t, f.appId, sourcemap.Update.AppId)
	assert.Equal(t, "main", sourcemap.Update.Branch)
	assert.Equal(t, "1", sourcemap.Update.RuntimeVersion)
	assert.Equal(t, "100", sourcemap.Update.UpdateId)
	payload, err := json.Marshal(sourcemap)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "Update")
	assert.NotContains(t, string(payload), "branch")
	assert.NotContains(t, string(payload), f.appId)

	foreign, err := f.indexes.GetUpdateSourcemapByUUID(ctx, uuid.NewString(), updateUUID)
	require.NoError(t, err)
	assert.Nil(t, foreign, "another application cannot resolve the repair identity")
}
