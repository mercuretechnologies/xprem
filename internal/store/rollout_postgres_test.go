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
	"strconv"
	"testing"

	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres"
	"expo-open-ota/internal/database/postgres/pgdb"
	"expo-open-ota/internal/store"
	"expo-open-ota/internal/types"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rolloutTestDefaultBranch = "main"
	rolloutTestRolloutBranch = "next"
	rolloutTestRuntime       = "1.0.0"
	rolloutTestChannel       = "production"
)

// rolloutFixture is one isolated app (fresh UUID per test) with two branches, a runtime
// version, and a channel mapped to the default branch.
type rolloutFixture struct {
	pool            *pgxpool.Pool
	rollouts        *store.PostgresRolloutStore
	updates         *store.PostgresUpdateStore
	branches        *store.PostgresBranchStore
	channels        *store.PostgresChannelStore
	appId           string
	defaultBranchId int64
	rolloutBranchId int64
	channelId       int64
}

func newRolloutFixture(t *testing.T) *rolloutFixture {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI: these tests cover SQL that the in-memory fakes cannot reach")
		}
		t.Skip("TEST_DATABASE_URL not set; start a Postgres and set it to run the rollout store tests")
	}
	// The seed migration fails fast on an empty database without the bootstrap pair.
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(dbURL)

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	engine := &database.Engine{Queries: pgdb.New(pool), DB: pool}

	ctx := context.Background()
	fixture := &rolloutFixture{
		pool:     pool,
		rollouts: store.NewPostgresRolloutStore(engine),
		updates:  store.NewPostgresUpdateStore(engine),
		branches: store.NewPostgresBranchStore(engine),
		channels: store.NewPostgresChannelStore(engine),
		appId:    uuid.NewString(),
	}
	_, err = pool.Exec(ctx, "INSERT INTO apps (id, name) VALUES ($1, $2)", fixture.appId, "rollout-store-test")
	require.NoError(t, err)
	fixture.defaultBranchId, err = fixture.branches.InsertBranch(ctx, pgdb.InsertBranchParams{AppID: store.ToPgUUID(fixture.appId), Name: rolloutTestDefaultBranch})
	require.NoError(t, err)
	fixture.rolloutBranchId, err = fixture.branches.InsertBranch(ctx, pgdb.InsertBranchParams{AppID: store.ToPgUUID(fixture.appId), Name: rolloutTestRolloutBranch})
	require.NoError(t, err)
	_, err = fixture.branches.CreateRuntimeVersion(ctx, fixture.appId, rolloutTestRuntime)
	require.NoError(t, err)
	fixture.channelId, err = fixture.channels.InsertChannel(ctx, fixture.appId, &fixture.defaultBranchId, rolloutTestChannel)
	require.NoError(t, err)
	return fixture
}

func (f *rolloutFixture) startChannelRollout(t *testing.T, percentage int) string {
	t.Helper()
	rolloutId := uuid.NewString()
	rows, err := f.rollouts.CreateChannelRollout(context.Background(), rolloutId, f.appId, rolloutTestChannel, rolloutTestRolloutBranch, percentage)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	return rolloutId
}

func (f *rolloutFixture) createUpdate(t *testing.T, branch string, updateId int64, platform string, checked bool) types.Update {
	t.Helper()
	created, err := f.updates.CreateUpdate(context.Background(), f.appId, updateId, branch, rolloutTestRuntime, platform, "abc123", "", nil)
	require.NoError(t, err)
	if checked {
		require.NoError(t, f.updates.MarkUpdateAsChecked(context.Background(), *created))
	}
	return *created
}

func (f *rolloutFixture) createRolloutUpdate(t *testing.T, branch string, updateId int64, platform string, percentage int) types.Update {
	t.Helper()
	created, err := f.updates.CreateUpdateWithRollout(context.Background(), f.appId, updateId, branch, rolloutTestRuntime, platform, "abc123", "", percentage, nil)
	require.NoError(t, err)
	return *created
}

func TestChannelRolloutInsertGuardsPostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	rolloutId := fixture.startChannelRollout(t, 25)
	activeRollout, err := fixture.rollouts.GetChannelRollout(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	require.NotNil(t, activeRollout)
	assert.Equal(t, rolloutId, activeRollout.ID)
	assert.Equal(t, rolloutTestChannel, activeRollout.ChannelName)
	assert.Equal(t, rolloutTestDefaultBranch, activeRollout.DefaultBranchName)
	assert.Equal(t, rolloutTestRolloutBranch, activeRollout.RolloutBranchName)
	assert.Equal(t, 25, activeRollout.Percentage)

	// One active rollout per channel is DB-enforced (UNIQUE on channel_id).
	_, err = fixture.rollouts.CreateChannelRollout(ctx, uuid.NewString(), fixture.appId, rolloutTestChannel, rolloutTestRolloutBranch, 10)
	alreadyExistsErr := (*store.ErrResourceAlreadyExists)(nil)
	require.ErrorAs(t, err, &alreadyExistsErr)

	rows, err := fixture.rollouts.DeleteChannelRollout(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	// The guarded INSERT...SELECT reports 0 rows for every refused start.
	rows, err = fixture.rollouts.CreateChannelRollout(ctx, uuid.NewString(), fixture.appId, rolloutTestChannel, rolloutTestDefaultBranch, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows, "rollout branch equal to the channel's default must be refused")

	rows, err = fixture.rollouts.CreateChannelRollout(ctx, uuid.NewString(), fixture.appId, "unknown-channel", rolloutTestRolloutBranch, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows, "unknown channel must be refused")

	rows, err = fixture.rollouts.CreateChannelRollout(ctx, uuid.NewString(), fixture.appId, rolloutTestChannel, "unknown-branch", 10)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows, "unknown rollout branch must be refused")

	_, err = fixture.channels.InsertChannel(ctx, fixture.appId, nil, "unmapped-channel")
	require.NoError(t, err)
	rows, err = fixture.rollouts.CreateChannelRollout(ctx, uuid.NewString(), fixture.appId, "unmapped-channel", rolloutTestRolloutBranch, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows, "channel without a mapped branch must be refused")

	// The 1-99 CHECK backs the service validation.
	_, err = fixture.rollouts.CreateChannelRollout(ctx, uuid.NewString(), fixture.appId, rolloutTestChannel, rolloutTestRolloutBranch, 0)
	assert.Error(t, err, "percentage 0 must violate the CHECK constraint")
}

func TestChannelRolloutLifecyclePostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	fixture.createUpdate(t, rolloutTestDefaultBranch, 900, "ios", true)
	fixture.createUpdate(t, rolloutTestRolloutBranch, 901, "ios", true)
	fixture.startChannelRollout(t, 10)

	channels, err := fixture.channels.GetChannels(ctx, fixture.appId)
	require.NoError(t, err)
	require.Len(t, channels, 1)
	require.NotNil(t, channels[0].BranchCurrentUpdate)
	assert.Equal(t, rolloutTestRuntime, channels[0].BranchCurrentUpdate.RuntimeVersion)
	assert.Equal(t, "abc123", channels[0].BranchCurrentUpdate.CommitHash)
	require.NotNil(t, channels[0].RolloutBranchCurrentUpdate)
	assert.Equal(t, rolloutTestRuntime, channels[0].RolloutBranchCurrentUpdate.RuntimeVersion)
	assert.Equal(t, "abc123", channels[0].RolloutBranchCurrentUpdate.CommitHash)

	rows, err := fixture.rollouts.UpdateChannelRolloutPercentage(ctx, fixture.appId, rolloutTestChannel, 50)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	activeRollout, err := fixture.rollouts.GetChannelRollout(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	require.NotNil(t, activeRollout)
	assert.Equal(t, 50, activeRollout.Percentage)

	rows, err = fixture.rollouts.DeleteChannelRollout(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	// Once ended, every guarded mutation reports 0 rows and the read reports nil.
	rows, err = fixture.rollouts.DeleteChannelRollout(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows)
	rows, err = fixture.rollouts.UpdateChannelRolloutPercentage(ctx, fixture.appId, rolloutTestChannel, 60)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows)
	activeRollout, err = fixture.rollouts.GetChannelRollout(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	assert.Nil(t, activeRollout)
}

func TestPromoteChannelRolloutPostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	fixture.startChannelRollout(t, 30)
	// The hot-path mapping read folds the active rollout in.
	mapping, err := fixture.channels.GetChannelBranchMapping(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	require.NotNil(t, mapping)
	assert.Equal(t, rolloutTestDefaultBranch, mapping.BranchName)
	require.NotNil(t, mapping.Rollout)
	assert.Equal(t, rolloutTestRolloutBranch, mapping.Rollout.BranchName)
	assert.Equal(t, 30, mapping.Rollout.Percentage)

	rows, err := fixture.rollouts.PromoteChannelRollout(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	mapping, err = fixture.channels.GetChannelBranchMapping(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	require.NotNil(t, mapping)
	assert.Equal(t, rolloutTestRolloutBranch, mapping.BranchName)
	assert.Nil(t, mapping.Rollout)
	activeRollout, err := fixture.rollouts.GetChannelRollout(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	assert.Nil(t, activeRollout)

	rows, err = fixture.rollouts.PromoteChannelRollout(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows, "promoting again must find nothing to repoint")
}

func TestChannelRolloutBranchAndMappingGuardsPostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	fixture.startChannelRollout(t, 20)

	// FK RESTRICT: a branch serving an active rollout cannot be deleted.
	err := fixture.branches.DeleteBranchByName(ctx, fixture.appId, rolloutTestRolloutBranch)
	require.Error(t, err)
	assert.True(t, database.IsForeignKeyViolation(err), "expected a foreign key violation, got: %v", err)

	channelNames, err := fixture.rollouts.GetChannelRolloutsByBranch(ctx, fixture.appId, rolloutTestRolloutBranch)
	require.NoError(t, err)
	assert.Equal(t, []string{rolloutTestChannel}, channelNames)

	// The guarded remap refuses to touch a channel locked by an active rollout.
	channelIdStr := strconv.FormatInt(fixture.channelId, 10)
	rolloutBranchIdStr := strconv.FormatInt(fixture.rolloutBranchId, 10)
	err = fixture.branches.UpdateChannelBranchMapping(ctx, fixture.appId, channelIdStr, rolloutBranchIdStr)
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	require.ErrorAs(t, err, &notFoundErr)

	mapping, err := fixture.channels.GetChannelBranchMapping(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	require.NotNil(t, mapping)
	assert.Equal(t, rolloutTestDefaultBranch, mapping.BranchName, "the refused remap must not change the mapping")

	// Ending the rollout unlocks both the remap and the branch delete.
	rows, err := fixture.rollouts.DeleteChannelRollout(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	require.NoError(t, fixture.branches.UpdateChannelBranchMapping(ctx, fixture.appId, channelIdStr, rolloutBranchIdStr))
	mapping, err = fixture.channels.GetChannelBranchMapping(ctx, fixture.appId, rolloutTestChannel)
	require.NoError(t, err)
	require.NotNil(t, mapping)
	assert.Equal(t, rolloutTestRolloutBranch, mapping.BranchName)
}

func TestChannelRolloutEndsWithChannelDeletePostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	fixture.startChannelRollout(t, 40)
	require.NoError(t, fixture.channels.DeleteChannel(ctx, rolloutTestChannel, fixture.appId))

	// CASCADE ends the rollout with the channel.
	var remainingRollouts int64
	require.NoError(t, fixture.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM channel_rollouts cr JOIN branches b ON cr.rollout_branch_id = b.id WHERE b.app_id = $1",
		fixture.appId).Scan(&remainingRollouts))
	assert.EqualValues(t, 0, remainingRollouts)

	require.NoError(t, fixture.branches.DeleteBranchByName(ctx, fixture.appId, rolloutTestRolloutBranch))
}

func TestPerUpdateRolloutActiveIndexPostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	// An unchecked rollout row is inert: it neither trips the publish guard nor counts as active.
	firstRollout := fixture.createRolloutUpdate(t, rolloutTestDefaultBranch, 1000, "ios", 10)
	hasActive, err := fixture.updates.HasActiveRolloutUpdate(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime)
	require.NoError(t, err)
	assert.False(t, hasActive)
	activeRollouts, err := fixture.rollouts.GetActiveRolloutUpdates(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime)
	require.NoError(t, err)
	assert.Empty(t, activeRollouts)

	// The partial unique index only fires at checked time.
	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, firstRollout))
	hasActive, err = fixture.updates.HasActiveRolloutUpdate(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime)
	require.NoError(t, err)
	assert.True(t, hasActive)

	// A second rollout row on the same (branch, rtv, platform) violates uq_updates_active_rollout at checked time.
	secondRollout := fixture.createRolloutUpdate(t, rolloutTestDefaultBranch, 1001, "ios", 20)
	err = fixture.updates.MarkUpdateAsChecked(ctx, secondRollout)
	require.Error(t, err)
	assert.True(t, database.IsUniqueViolation(err), "expected a unique violation, got: %v", err)

	// The index is per platform: the android half of the same logical rollout passes.
	androidRollout := fixture.createRolloutUpdate(t, rolloutTestDefaultBranch, 1002, "android", 20)
	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, androidRollout))
}

func TestPerUpdateRolloutRowsAndCountsPostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	// One logical rollout is one row per platform, each with its own control.
	fixture.createUpdate(t, rolloutTestDefaultBranch, 2000, "ios", true)
	fixture.createUpdate(t, rolloutTestDefaultBranch, 2001, "android", true)
	iosRollout := fixture.createRolloutUpdate(t, rolloutTestDefaultBranch, 2010, "ios", 20)
	androidRollout := fixture.createRolloutUpdate(t, rolloutTestDefaultBranch, 2011, "android", 20)
	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, iosRollout))
	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, androidRollout))

	runtimeVersions, err := fixture.branches.GetRuntimeVersionsWithUpdateStats(ctx, fixture.appId, rolloutTestDefaultBranch)
	require.NoError(t, err)
	require.Len(t, runtimeVersions, 1)
	assert.True(t, runtimeVersions[0].ActiveRollout)
	require.NotNil(t, runtimeVersions[0].RolloutPercentage)
	assert.Equal(t, 20, *runtimeVersions[0].RolloutPercentage)

	branches, err := fixture.branches.GetBranches(ctx, fixture.appId)
	require.NoError(t, err)
	var defaultBranch *types.BranchMapping
	for i := range branches {
		if branches[i].BranchName == rolloutTestDefaultBranch {
			defaultBranch = &branches[i]
			break
		}
	}
	require.NotNil(t, defaultBranch)
	require.NotNil(t, defaultBranch.CurrentUpdate)
	assert.Equal(t, rolloutTestRuntime, defaultBranch.CurrentUpdate.RuntimeVersion)
	assert.Equal(t, "abc123", defaultBranch.CurrentUpdate.CommitHash)
	require.NotNil(t, defaultBranch.CurrentUpdate.RolloutPercentage)
	assert.Equal(t, 20, *defaultBranch.CurrentUpdate.RolloutPercentage)

	channels, err := fixture.channels.GetChannels(ctx, fixture.appId)
	require.NoError(t, err)
	require.Len(t, channels, 1)
	require.NotNil(t, channels[0].BranchCurrentUpdate)
	require.NotNil(t, channels[0].BranchCurrentUpdate.RolloutPercentage)
	assert.Equal(t, 20, *channels[0].BranchCurrentUpdate.RolloutPercentage)

	activeRollouts, err := fixture.rollouts.GetActiveRolloutUpdates(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime)
	require.NoError(t, err)
	require.Len(t, activeRollouts, 2)
	assert.Equal(t, "android", activeRollouts[0].Platform)
	assert.Equal(t, "2011", activeRollouts[0].UpdateId)
	require.NotNil(t, activeRollouts[0].ControlUpdateId)
	assert.Equal(t, "2001", *activeRollouts[0].ControlUpdateId)
	assert.Equal(t, "ios", activeRollouts[1].Platform)
	assert.Equal(t, "2010", activeRollouts[1].UpdateId)
	require.NotNil(t, activeRollouts[1].ControlUpdateId)
	assert.Equal(t, "2000", *activeRollouts[1].ControlUpdateId)

	// Progression and clear touch every active row of the pair.
	rows, err := fixture.rollouts.SetUpdateRolloutPercentage(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime, 55)
	require.NoError(t, err)
	require.EqualValues(t, 2, rows)
	activeRollouts, err = fixture.rollouts.GetActiveRolloutUpdates(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime)
	require.NoError(t, err)
	require.Len(t, activeRollouts, 2)
	assert.Equal(t, 55, activeRollouts[0].Percentage)
	assert.Equal(t, 55, activeRollouts[1].Percentage)
	runtimeVersions, err = fixture.branches.GetRuntimeVersionsWithUpdateStats(ctx, fixture.appId, rolloutTestDefaultBranch)
	require.NoError(t, err)
	require.NotNil(t, runtimeVersions[0].RolloutPercentage)
	assert.Equal(t, 55, *runtimeVersions[0].RolloutPercentage)

	rows, err = fixture.rollouts.ClearUpdateRollout(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime)
	require.NoError(t, err)
	require.EqualValues(t, 2, rows)
	activeRollouts, err = fixture.rollouts.GetActiveRolloutUpdates(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime)
	require.NoError(t, err)
	assert.Empty(t, activeRollouts)
	hasActive, err := fixture.updates.HasActiveRolloutUpdate(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime)
	require.NoError(t, err)
	assert.False(t, hasActive)
	runtimeVersions, err = fixture.branches.GetRuntimeVersionsWithUpdateStats(ctx, fixture.appId, rolloutTestDefaultBranch)
	require.NoError(t, err)
	assert.False(t, runtimeVersions[0].ActiveRollout)
	assert.Nil(t, runtimeVersions[0].RolloutPercentage)
	branches, err = fixture.branches.GetBranches(ctx, fixture.appId)
	require.NoError(t, err)
	for i := range branches {
		if branches[i].BranchName != rolloutTestDefaultBranch {
			continue
		}
		require.NotNil(t, branches[i].CurrentUpdate)
		assert.Nil(t, branches[i].CurrentUpdate.RolloutPercentage)
	}

	// Once cleared, the guarded mutations report 0 rows (the concurrent-end race).
	rows, err = fixture.rollouts.SetUpdateRolloutPercentage(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime, 60)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows)
	rows, err = fixture.rollouts.ClearUpdateRollout(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows)
}

func TestInsertUpdateWithRolloutControlResolutionPostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	// First update of a branch: there is no control.
	firstRollout := fixture.createRolloutUpdate(t, rolloutTestRolloutBranch, 3000, "ios", 10)
	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, firstRollout))
	envelope, err := fixture.updates.GetLatestUpdateWithRollout(ctx, fixture.appId, rolloutTestRolloutBranch, rolloutTestRuntime, "ios")
	require.NoError(t, err)
	require.NotNil(t, envelope)
	assert.Equal(t, "3000", envelope.UpdateId)
	require.NotNil(t, envelope.RolloutPercentage)
	assert.Equal(t, 10, *envelope.RolloutPercentage)
	assert.Nil(t, envelope.Control)

	// The control is the latest CHECKED update of the same platform.
	fixture.createUpdate(t, rolloutTestDefaultBranch, 3100, "ios", true)
	fixture.createUpdate(t, rolloutTestDefaultBranch, 3105, "android", true)
	fixture.createUpdate(t, rolloutTestDefaultBranch, 3110, "ios", true)
	fixture.createUpdate(t, rolloutTestDefaultBranch, 3120, "ios", false)
	mainRollout := fixture.createRolloutUpdate(t, rolloutTestDefaultBranch, 3130, "ios", 30)
	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, mainRollout))

	envelope, err = fixture.updates.GetLatestUpdateWithRollout(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime, "ios")
	require.NoError(t, err)
	require.NotNil(t, envelope)
	assert.Equal(t, "3130", envelope.UpdateId)
	require.NotNil(t, envelope.RolloutPercentage)
	assert.Equal(t, 30, *envelope.RolloutPercentage)
	require.NotNil(t, envelope.Control)
	assert.Equal(t, "3110", envelope.Control.UpdateId)

	activeRollouts, err := fixture.rollouts.GetActiveRolloutUpdates(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime)
	require.NoError(t, err)
	require.Len(t, activeRollouts, 1)
	require.NotNil(t, activeRollouts[0].ControlUpdateId)
	assert.Equal(t, "3110", *activeRollouts[0].ControlUpdateId)
}

func TestMarkUpdateAsCheckedConditionalStampPostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	// Direction 1: a plain update cannot become visible while a rollout is active
	// on its (branch, rtv, platform).
	fixture.createUpdate(t, rolloutTestDefaultBranch, 4000, "ios", true)
	activeRollout := fixture.createRolloutUpdate(t, rolloutTestDefaultBranch, 4010, "ios", 20)
	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, activeRollout))

	racingPlain := fixture.createUpdate(t, rolloutTestDefaultBranch, 4020, "ios", false)
	err := fixture.updates.MarkUpdateAsChecked(ctx, racingPlain)
	require.ErrorIs(t, err, store.ErrPublishBlockedByActiveRollout)
	visible, checkErr := fixture.updates.IsUpdateValid(ctx, racingPlain)
	require.NoError(t, checkErr)
	assert.False(t, visible, "the refused plain update must stay unchecked")

	// The guard is platform-scoped: the same race on android passes.
	otherPlatform := fixture.createUpdate(t, rolloutTestDefaultBranch, 4021, "android", false)
	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, otherPlatform))

	// Direction 2: a rollout update superseded by a newer checked update during its
	// upload must not activate.
	fixture.createUpdate(t, rolloutTestRolloutBranch, 5000, "ios", true)
	supersededRollout := fixture.createRolloutUpdate(t, rolloutTestRolloutBranch, 5010, "ios", 20)
	fixture.createUpdate(t, rolloutTestRolloutBranch, 5020, "ios", true)
	err = fixture.updates.MarkUpdateAsChecked(ctx, supersededRollout)
	require.ErrorIs(t, err, store.ErrRolloutSupersededByNewerUpdate)
	hasActive, activeErr := fixture.updates.HasActiveRolloutUpdate(ctx, fixture.appId, rolloutTestRolloutBranch, rolloutTestRuntime)
	require.NoError(t, activeErr)
	assert.False(t, hasActive, "the superseded rollout must not activate")
}
