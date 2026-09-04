package store_test

import (
	"context"
	"testing"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/store"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBundlePatchLifecyclePostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()
	patches := store.NewPostgresBundlePatchStore(&database.Engine{Queries: pgdb.New(fixture.pool), DB: fixture.pool})
	fixture.createUpdate(t, rolloutTestDefaultBranch, 100, types.PlatformAndroid, true)
	fixture.createUpdate(t, rolloutTestDefaultBranch, 150, types.PlatformAndroid, true)
	fixture.createUpdate(t, rolloutTestDefaultBranch, 200, types.PlatformAndroid, true)

	require.NoError(t, patches.MarkPending(ctx, fixture.appId, rolloutTestDefaultBranch, "200", "100"))
	require.NoError(t, patches.MarkPending(ctx, fixture.appId, rolloutTestDefaultBranch, "200", "150"))

	rows, err := patches.ListByTarget(ctx, fixture.appId, rolloutTestDefaultBranch, "200")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "150", rows[0].SourceUpdateId, "newest source first")
	assert.Equal(t, "100", rows[1].SourceUpdateId)
	assert.Equal(t, types.BundlePatchPending, rows[1].Status)
	assert.Equal(t, 0, rows[1].Attempts)

	require.NoError(t, patches.MarkRunning(ctx, fixture.appId, rolloutTestDefaultBranch, "200", "100"))
	patchSize, fullSize := int64(1867), int64(1189158)
	require.NoError(t, patches.Finish(ctx, fixture.appId, rolloutTestDefaultBranch, "200", "100", types.BundlePatchStored, "", &patchSize, &fullSize))
	rows, err = patches.ListByTarget(ctx, fixture.appId, rolloutTestDefaultBranch, "200")
	require.NoError(t, err)
	stored := rows[1]
	assert.Equal(t, types.BundlePatchStored, stored.Status)
	assert.Equal(t, 1, stored.Attempts)
	assert.Empty(t, stored.Reason)
	require.NotNil(t, stored.PatchSize)
	assert.EqualValues(t, 1867, *stored.PatchSize)
	require.NotNil(t, stored.FullDownloadSize)
	assert.EqualValues(t, 1189158, *stored.FullDownloadSize)

	// A recompute resets the pair.
	require.NoError(t, patches.MarkPending(ctx, fixture.appId, rolloutTestDefaultBranch, "200", "100"))
	rows, err = patches.ListByTarget(ctx, fixture.appId, rolloutTestDefaultBranch, "200")
	require.NoError(t, err)
	assert.Equal(t, types.BundlePatchPending, rows[1].Status)
	assert.Nil(t, rows[1].PatchSize)
	assert.Equal(t, 0, rows[1].Attempts)

	require.NoError(t, patches.Finish(ctx, fixture.appId, rolloutTestDefaultBranch, "200", "150", types.BundlePatchSkipped, types.BundlePatchReasonNotWorth, &patchSize, &fullSize))
	rows, err = patches.ListByTarget(ctx, fixture.appId, rolloutTestDefaultBranch, "200")
	require.NoError(t, err)
	assert.Equal(t, types.BundlePatchSkipped, rows[0].Status)
	assert.Equal(t, types.BundlePatchReasonNotWorth, rows[0].Reason)

	// Scoped by app: another app's id cannot reach the branch.
	assert.Error(t, patches.MarkPending(ctx, "00000000-0000-0000-0000-000000000000", rolloutTestDefaultBranch, "200", "100"))

	// The record dies with either update.
	_, err = fixture.pool.Exec(ctx, "DELETE FROM updates WHERE branch_id = $1 AND id = 150", fixture.defaultBranchId)
	require.NoError(t, err)
	rows, err = patches.ListByTarget(ctx, fixture.appId, rolloutTestDefaultBranch, "200")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "100", rows[0].SourceUpdateId)
	_, err = fixture.pool.Exec(ctx, "DELETE FROM updates WHERE branch_id = $1 AND id = 200", fixture.defaultBranchId)
	require.NoError(t, err)
	rows, err = patches.ListByTarget(ctx, fixture.appId, rolloutTestDefaultBranch, "200")
	require.NoError(t, err)
	require.Empty(t, rows)
}
