// Integration tests for publish_group persistence: the column is written by
// both insert paths (plain publish, rollout publish) and surfaced by the
// branch listing. Same TEST_DATABASE_URL gating as the rollout store tests.
package store_test

import (
	"context"
	"expo-open-ota/internal/types"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checkedUpdate publishes and checks one update, stamping a stored uuid so the
// listing resolves it without reaching for bucket metadata.
func (f *rolloutFixture) checkedUpdate(t *testing.T, updateId int64, platform string, publishGroup *string) {
	t.Helper()
	ctx := context.Background()
	created, err := f.updates.CreateUpdate(ctx, f.appId, updateId, rolloutTestDefaultBranch, rolloutTestRuntime, platform, "abc123", "", publishGroup)
	require.NoError(t, err)
	require.NoError(t, f.updates.MarkUpdateAsChecked(ctx, *created))
	require.NoError(t, f.updates.StoreUpdateUUIDInMetadata(ctx, *created, uuid.NewString()))
}

func TestPublishGroupPersistencePostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	// One publish run: two platform rows sharing the CLI-minted group. A row
	// published without one (older CLI) stays ungrouped.
	publishGroup := uuid.NewString()
	fixture.checkedUpdate(t, 100, "ios", &publishGroup)
	fixture.checkedUpdate(t, 200, "android", &publishGroup)
	fixture.checkedUpdate(t, 300, "ios", nil)

	// Rollbacks are branch-level operations, never grouped: the marker row
	// must list with no publish group.
	rollback, err := fixture.updates.CreateRollback(ctx, fixture.appId, 400, rolloutTestDefaultBranch, rolloutTestRuntime, "android", "abc123")
	require.NoError(t, err)
	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, *rollback))

	// A rollout publish goes through the dedicated insert; group stored the same.
	rolloutGroup := uuid.NewString()
	rolloutUpdate, err := fixture.updates.CreateUpdateWithRollout(ctx, fixture.appId, 500, rolloutTestDefaultBranch, rolloutTestRuntime, "ios", "abc123", "", 25, &rolloutGroup)
	require.NoError(t, err)
	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, *rolloutUpdate))
	require.NoError(t, fixture.updates.StoreUpdateUUIDInMetadata(ctx, *rolloutUpdate, uuid.NewString()))

	items, err := fixture.updates.GetUpdatesByRunTimeVersionAndBranchName(ctx, fixture.appId, rolloutTestRuntime, rolloutTestDefaultBranch)
	require.NoError(t, err)
	require.Len(t, items, 5)

	groupsById := map[string]*string{}
	for _, item := range items {
		groupsById[item.UpdateId] = item.PublishGroup
	}
	require.NotNil(t, groupsById["100"])
	require.NotNil(t, groupsById["200"])
	assert.Equal(t, publishGroup, *groupsById["100"])
	assert.Equal(t, publishGroup, *groupsById["200"])
	assert.Nil(t, groupsById["300"])
	assert.Nil(t, groupsById["400"])
	require.NotNil(t, groupsById["500"])
	assert.Equal(t, rolloutGroup, *groupsById["500"])
}

func TestGetUpdatesByPublishGroupPostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	group := uuid.NewString()
	fixture.checkedUpdate(t, 100, "ios", &group)
	fixture.checkedUpdate(t, 200, "android", &group)
	// An unchecked member is an unfinished upload: invisible to group operations.
	_, err := fixture.updates.CreateUpdate(ctx, fixture.appId, 300, rolloutTestDefaultBranch, rolloutTestRuntime, "ios", "abc123", "", &group)
	require.NoError(t, err)
	// Another group on the same branch stays out of the result.
	otherGroup := uuid.NewString()
	fixture.checkedUpdate(t, 400, "ios", &otherGroup)

	members, err := fixture.updates.GetUpdatesByPublishGroup(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime, group)
	require.NoError(t, err)
	require.Len(t, members, 2)
	assert.Equal(t, "100", members[0].UpdateId)
	assert.Equal(t, "ios", members[0].Platform)
	assert.Equal(t, "abc123", members[0].CommitHash)
	assert.Equal(t, "200", members[1].UpdateId)
	assert.Equal(t, "android", members[1].Platform)

	none, err := fixture.updates.GetUpdatesByPublishGroup(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime, uuid.NewString())
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestGetUpdateFeedPostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	group := uuid.NewString()
	fixture.checkedUpdate(t, 100, "ios", &group)
	fixture.checkedUpdate(t, 200, "android", &group)
	next, err := fixture.updates.CreateUpdate(ctx, fixture.appId, 300, rolloutTestRolloutBranch, rolloutTestRuntime, "ios", "def456", "next branch", nil)
	require.NoError(t, err)
	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, *next))
	nextUUID := uuid.NewString()
	require.NoError(t, fixture.updates.StoreUpdateUUIDInMetadata(ctx, *next, nextUUID))
	_, err = fixture.updates.CreateUpdate(ctx, fixture.appId, 400, rolloutTestDefaultBranch, rolloutTestRuntime, "ios", "unfinished", "", nil)
	require.NoError(t, err)

	items, err := fixture.updates.GetUpdateFeed(ctx, fixture.appId, types.UpdateFeedQuery{Limit: 100})
	require.NoError(t, err)
	require.Len(t, items, 3, "unchecked uploads must stay out of the dashboard feed")

	byID := make(map[string]types.UpdateFeedItem, len(items))
	for _, item := range items {
		byID[item.UpdateId] = item
	}
	assert.Equal(t, rolloutTestDefaultBranch, byID["100"].Branch)
	assert.Equal(t, rolloutTestRuntime, byID["100"].RuntimeVersion)
	require.NotNil(t, byID["100"].PublishGroup)
	assert.Equal(t, group, *byID["100"].PublishGroup)
	assert.Equal(t, rolloutTestRolloutBranch, byID["300"].Branch)
	assert.Equal(t, nextUUID, byID["300"].UpdateUUID)
	assert.Equal(t, "next branch", byID["300"].Message)

	groupItems, err := fixture.updates.GetUpdateFeed(ctx, fixture.appId, types.UpdateFeedQuery{
		PublishGroup: group,
		Limit:        100,
	})
	require.NoError(t, err)
	require.Len(t, groupItems, 2)

	firstPage, err := fixture.updates.GetUpdateFeed(ctx, fixture.appId, types.UpdateFeedQuery{Limit: 2})
	require.NoError(t, err)
	require.Len(t, firstPage, 2)
	cursorUpdateID, err := strconv.ParseInt(firstPage[1].UpdateId, 10, 64)
	require.NoError(t, err)
	secondPage, err := fixture.updates.GetUpdateFeed(ctx, fixture.appId, types.UpdateFeedQuery{
		CursorCreatedAt: &firstPage[1].FeedCreatedAt,
		CursorBranchID:  firstPage[1].BranchID,
		CursorUpdateID:  cursorUpdateID,
		Limit:           2,
	})
	require.NoError(t, err)
	require.Len(t, secondPage, 1)
	assert.NotEqual(t, firstPage[0].UpdateId, secondPage[0].UpdateId)
	assert.NotEqual(t, firstPage[1].UpdateId, secondPage[0].UpdateId)
}

// TestPublishGroupRolloutActivationPostgres pins the sequential worst case of
// one grouped rollout publish against the real SQL guards: iOS's rollout is
// already active (checked) when Android's stamp runs. Both the conditional
// stamp and the partial unique index are scoped per (branch, rtv, platform),
// so the second platform of the same run must activate, leaving two active
// rollouts under one publish group.
func TestPublishGroupRolloutActivationPostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	group := uuid.NewString()
	ios, err := fixture.updates.CreateUpdateWithRollout(ctx, fixture.appId, 600, rolloutTestDefaultBranch, rolloutTestRuntime, "ios", "abc123", "", 10, &group)
	require.NoError(t, err)
	android, err := fixture.updates.CreateUpdateWithRollout(ctx, fixture.appId, 700, rolloutTestDefaultBranch, rolloutTestRuntime, "android", "abc123", "", 10, &group)
	require.NoError(t, err)

	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, *ios))
	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, *android),
		"the second platform of a grouped rollout publish must not be blocked by the first one's active rollout")

	active, err := fixture.updates.HasActiveRolloutUpdate(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime)
	require.NoError(t, err)
	assert.True(t, active)

	require.NoError(t, fixture.updates.StoreUpdateUUIDInMetadata(ctx, *ios, uuid.NewString()))
	require.NoError(t, fixture.updates.StoreUpdateUUIDInMetadata(ctx, *android, uuid.NewString()))
	items, err := fixture.updates.GetUpdatesByRunTimeVersionAndBranchName(ctx, fixture.appId, rolloutTestRuntime, rolloutTestDefaultBranch)
	require.NoError(t, err)
	require.Len(t, items, 2)
	for _, item := range items {
		require.NotNil(t, item.PublishGroup)
		assert.Equal(t, group, *item.PublishGroup)
		require.NotNil(t, item.RolloutPercentage)
		assert.Equal(t, 10, *item.RolloutPercentage)
	}

	feed, err := fixture.updates.GetUpdateFeed(ctx, fixture.appId, types.UpdateFeedQuery{
		PublishGroup: group,
		Limit:        10,
	})
	require.NoError(t, err)
	require.Len(t, feed, 2)
	for _, item := range feed {
		require.NotNil(t, item.RolloutPercentage)
		assert.Equal(t, 10, *item.RolloutPercentage)
	}
}
