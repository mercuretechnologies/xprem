package services

import (
	"context"
	"errors"
	"testing"
	"xprem/internal/cache"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const surfingCacheAppID = "branch-surfing-cache-test"

func surfingCacheService(t *testing.T, surfing map[string]*types.BranchSurfing, repoErr error) (*ExpoProtocolService, *countingChannelRepo) {
	t.Helper()
	t.Setenv("DB_URL", "postgres://stub")
	repo := &countingChannelRepo{
		fakeChannelRepo: &fakeChannelRepo{surfing: surfing},
		surfingErr:      repoErr,
	}
	// The cache is a process-wide singleton, so an entry left by another test
	// would answer before the repository does.
	for _, channelName := range []string{"qa", "nope"} {
		cache.GetCache().Delete(channelBranchSurfingCacheKey(surfingCacheAppID, channelName))
	}
	return NewExpoProtocolService(fakeAppRepo{}, repo, nil, nil, nil), repo
}

func TestBranchSurfingEnabledCachesTheRead(t *testing.T) {
	service, repo := surfingCacheService(t, map[string]*types.BranchSurfing{
		"qa": {Enabled: true, Pattern: "pr-*"},
	}, nil)

	enabledQA, pattern := service.branchSurfingEnabled(context.Background(), surfingCacheAppID, "qa")
	assert.True(t, enabledQA)
	assert.Equal(t, "pr-*", pattern)
	enabledQA, pattern = service.branchSurfingEnabled(context.Background(), surfingCacheAppID, "qa")
	assert.True(t, enabledQA)
	assert.Equal(t, "pr-*", pattern)
	assert.Equal(t, 1, repo.surfingReads, "the second poll must be served from the cache")
}

// A disabled channel is cached too: the manifest path must not read the
// database on every poll just because the answer is false.
func TestBranchSurfingDisabledIsCachedAsWell(t *testing.T) {
	service, repo := surfingCacheService(t, map[string]*types.BranchSurfing{
		"qa": {Enabled: false, Pattern: "*"},
	}, nil)

	enabledQA, pattern := service.branchSurfingEnabled(context.Background(), surfingCacheAppID, "qa")
	assert.False(t, enabledQA)
	assert.Equal(t, "*", pattern)

	enabledQA, pattern = service.branchSurfingEnabled(context.Background(), surfingCacheAppID, "qa")

	assert.Equal(t, 1, repo.surfingReads)
}

func TestBranchSurfingCacheIsInvalidatedOnWrite(t *testing.T) {
	surfing := map[string]*types.BranchSurfing{"qa": {Enabled: false, Pattern: "*"}}
	service, repo := surfingCacheService(t, surfing, nil)
	enabledQA, _ := service.branchSurfingEnabled(context.Background(), surfingCacheAppID, "qa")
	require.False(t, enabledQA)
	assert.Equal(t, 1, repo.surfingReads)

	surfing["qa"] = &types.BranchSurfing{Enabled: true, Pattern: "pr-*"}
	invalidateBranchSurfingCache(surfingCacheAppID, "qa")

	enabledQA, _ = service.branchSurfingEnabled(context.Background(), surfingCacheAppID, "qa")

	assert.True(t, enabledQA)
	assert.Equal(t, 2, repo.surfingReads)
}

// The manifest hot path must never fail on this lookup: a broken read answers
// "not surfable" rather than turning a poll into a 500.
func TestBranchSurfingErrorAnswersFalse(t *testing.T) {
	service, _ := surfingCacheService(t, nil, errors.New("database is down"))

	enabledQA, _ := service.branchSurfingEnabled(context.Background(), surfingCacheAppID, "qa")
	assert.False(t, enabledQA)
}

func TestBranchSurfingUnknownChannelAnswersFalse(t *testing.T) {
	service, _ := surfingCacheService(t, nil, nil)

	enabledNope, _ := service.branchSurfingEnabled(context.Background(), surfingCacheAppID, "nope")

	assert.False(t, enabledNope)
}

// Stateless mode has no channel settings at all, so the lookup is skipped
// rather than reaching a repository that would refuse it.
func TestBranchSurfingIsSkippedInStatelessMode(t *testing.T) {
	service, repo := surfingCacheService(t, map[string]*types.BranchSurfing{
		"qa": {Enabled: true, Pattern: "*"},
	}, nil)
	t.Setenv("DB_URL", "")
	enabledQA, _ := service.branchSurfingEnabled(context.Background(), surfingCacheAppID, "qa")

	assert.False(t, enabledQA)
	assert.Equal(t, 0, repo.surfingReads)
}
