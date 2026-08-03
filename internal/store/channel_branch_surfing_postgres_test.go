// Integration tests for the branch-surfing SQL: the channel setting round trip and
// GetSurfableBranches, whose GROUP BY / MAX() and runtime-version scoping the
// in-memory fakes cannot exercise. Same harness as branch_postgres_test.go: they skip
// unless TEST_DATABASE_URL is set.
package store_test

import (
	"context"
	"testing"
	"time"

	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/store"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupChannelStore(t *testing.T) (*store.PostgresChannelStore, *pgxpool.Pool) {
	t.Helper()
	_, pool := setupBranchStore(t)
	return store.NewPostgresChannelStore(&database.Engine{Queries: pgdb.New(pool), DB: pool}), pool
}

func insertChannel(t *testing.T, pool *pgxpool.Pool, appId, channelName string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		"INSERT INTO channels (app_id, name) VALUES ($1, $2)", appId, channelName)
	require.NoError(t, err)
}

// seedUpdate puts one published update on a branch for a runtime version. createdAt is
// explicit so the ordering assertions do not depend on insertion speed.
func seedUpdate(t *testing.T, pool *pgxpool.Pool, appId, branchName, runtimeVersion string, id int64, createdAt time.Time) {
	t.Helper()
	ctx := context.Background()
	var branchId int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO branches (app_id, name) VALUES ($1, $2)
		 ON CONFLICT (app_id, name) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
		appId, branchName).Scan(&branchId))

	var runtimeVersionId int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO runtime_versions (app_id, version) VALUES ($1, $2)
		 ON CONFLICT (app_id, version) DO UPDATE SET version = EXCLUDED.version RETURNING id`,
		appId, runtimeVersion).Scan(&runtimeVersionId))

	_, err := pool.Exec(ctx,
		`INSERT INTO updates (id, branch_id, runtime_version_id, update_type, commit_hash, platform, created_at, checked_at)
		 VALUES ($1, $2, $3, 0, 'deadbeef', 'ios', $4, $4)`,
		id, branchId, runtimeVersionId, createdAt)
	require.NoError(t, err)
}

func TestBranchSurfingSettingRoundTrip(t *testing.T) {
	channelStore, pool := setupChannelStore(t)
	ctx := context.Background()
	appId := insertAppWithBranch(t, pool, "staging", false)
	insertChannel(t, pool, appId, "qa")

	// A channel starts closed AND with a pattern that names nothing: the default
	// decides what a first careless click would expose, so it must not be "*".
	initial, err := channelStore.GetBranchSurfing(ctx, appId, "qa")
	require.NoError(t, err)
	require.NotNil(t, initial)
	assert.Equal(t, types.BranchSurfing{Enabled: false, Pattern: ""}, *initial)

	require.NoError(t, channelStore.SetBranchSurfing(ctx, appId, "qa",
		types.BranchSurfing{Enabled: true, Pattern: "pr-*"}))

	updated, err := channelStore.GetBranchSurfing(ctx, appId, "qa")
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, types.BranchSurfing{Enabled: true, Pattern: "pr-*"}, *updated)
}

// An unknown channel is (nil, nil), not an error: the caller turns that into the same
// 404 a disabled channel gets.
func TestGetBranchSurfingOnUnknownChannel(t *testing.T) {
	channelStore, pool := setupChannelStore(t)
	appId := insertAppWithBranch(t, pool, "staging", false)

	surfing, err := channelStore.GetBranchSurfing(context.Background(), appId, "ghost")

	require.NoError(t, err)
	assert.Nil(t, surfing)
}

func TestSetBranchSurfingOnUnknownChannel(t *testing.T) {
	channelStore, pool := setupChannelStore(t)
	appId := insertAppWithBranch(t, pool, "staging", false)

	err := channelStore.SetBranchSurfing(context.Background(), appId, "ghost",
		types.BranchSurfing{Enabled: true, Pattern: "*"})

	notFoundErr := (*store.ErrResourceNotFound)(nil)
	assert.ErrorAs(t, err, &notFoundErr)
}

func TestGetSurfableBranchesScopesToRuntimeVersionAndOrdersByRecency(t *testing.T) {
	branchStore, pool := setupBranchStore(t)
	ctx := context.Background()
	appId := insertAppWithBranch(t, pool, "staging", false)
	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	seedUpdate(t, pool, appId, "pr-31", "3.0.0", 1, base)
	seedUpdate(t, pool, appId, "pr-482", "3.0.0", 2, base.Add(2*time.Hour))
	// Two updates on one branch: MAX() must report the newest, and the branch must
	// appear once.
	seedUpdate(t, pool, appId, "pr-482", "3.0.0", 3, base.Add(4*time.Hour))
	// Another runtime version, invisible to a 3.0.0 device.
	seedUpdate(t, pool, appId, "legacy", "2.0.0", 4, base.Add(6*time.Hour))

	branches, err := branchStore.GetSurfableBranches(ctx, appId, "3.0.0")

	require.NoError(t, err)
	assert.Equal(t, []types.SurfableBranch{
		{Name: "pr-482", LastUpdateAt: base.Add(4 * time.Hour).Format(time.RFC3339)},
		{Name: "pr-31", LastUpdateAt: base.Format(time.RFC3339)},
	}, branches)
}

// A branch whose only update was never checked has nothing servable on it, so it is
// not a surfing target.
func TestGetSurfableBranchesSkipsUncheckedUpdates(t *testing.T) {
	branchStore, pool := setupBranchStore(t)
	ctx := context.Background()
	appId := insertAppWithBranch(t, pool, "staging", false)
	seedUpdate(t, pool, appId, "pr-482", "3.0.0", 1, time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC))
	_, err := pool.Exec(ctx, "UPDATE updates SET checked_at = NULL")
	require.NoError(t, err)

	branches, err := branchStore.GetSurfableBranches(ctx, appId, "3.0.0")

	require.NoError(t, err)
	assert.Empty(t, branches)
}

// Version strings are unique per app, not globally: another tenant's 3.0.0 must not
// leak its branches into this app's list.
func TestGetSurfableBranchesIsScopedToTheApp(t *testing.T) {
	branchStore, pool := setupBranchStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	mine := insertAppWithBranch(t, pool, "staging", false)
	theirs := uuid.NewString()
	_, err := pool.Exec(ctx, "INSERT INTO apps (id, name) VALUES ($1, $2)", theirs, "other")
	require.NoError(t, err)

	seedUpdate(t, pool, mine, "pr-1", "3.0.0", 1, base)
	seedUpdate(t, pool, theirs, "pr-2", "3.0.0", 2, base)

	branches, err := branchStore.GetSurfableBranches(ctx, mine, "3.0.0")

	require.NoError(t, err)
	require.Len(t, branches, 1)
	assert.Equal(t, "pr-1", branches[0].Name)
}
