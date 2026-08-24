package services

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	cache2 "xprem/internal/cache"
	"xprem/internal/providers/expo"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const surfingAppID = "1a2b3c4d-0000-0000-0000-000000000001"

// Both reads are cached now, and the cache is a process-wide singleton keyed on
// the app id, so each test starts from a cleared one or reads its neighbour's
// answer. DB_URL has to look set: surfing only exists on the control plane.
func surfingService(t *testing.T, surfing map[string]*types.BranchSurfing, surfable map[string][]types.SurfableBranch) (*ChannelService, *fakeChannelRepo) {
	t.Helper()
	t.Setenv("DB_URL", "postgres://stub")
	for _, channelName := range []string{"qa", "production", "nope", "qa/../prod"} {
		ForgetBranchSurfing(surfingAppID, channelName)
	}
	for _, runtimeVersion := range []string{"3.0.0", "2.0.0"} {
		ForgetSurfableBranches(surfingAppID, runtimeVersion, "ios")
	}
	for _, channelName := range []string{"qa", "production"} {
		cache2.GetCache().Delete(channelMappingCacheKey(surfingAppID, channelName))
	}
	channelRepo := &fakeChannelRepo{surfing: surfing}
	return NewChannelService(fakeBranchRepo{surfable: surfable}, channelRepo), channelRepo
}

func TestListSurfableBranchesFiltersOnPattern(t *testing.T) {
	service, _ := surfingService(
		t,
		map[string]*types.BranchSurfing{"qa": {Enabled: true, Pattern: "pr-*"}},
		map[string][]types.SurfableBranch{"3.0.0": {
			{Name: "pr-482", LastUpdateAt: "2026-08-01T10:00:00Z"},
			{Name: "production", LastUpdateAt: "2026-07-30T10:00:00Z"},
			{Name: "pr-31", LastUpdateAt: "2026-07-29T10:00:00Z"},
		}},
	)

	list, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", "ios", false)

	require.NoError(t, err)
	assert.Equal(t, []types.SurfableBranch{
		{Name: "pr-482", LastUpdateAt: "2026-08-01T10:00:00Z"},
		{Name: "pr-31", LastUpdateAt: "2026-07-29T10:00:00Z"},
	}, list.Branches)
}

// The runtime version is what the repository is asked for: a branch holding no
// update for the device's runtime version is unreachable, so it must never be
// offered.
func TestListSurfableBranchesScopesToRuntimeVersion(t *testing.T) {
	service, _ := surfingService(
		t,
		map[string]*types.BranchSurfing{"qa": {Enabled: true, Pattern: "*"}},
		map[string][]types.SurfableBranch{
			"3.0.0": {{Name: "pr-482", LastUpdateAt: "2026-08-01T10:00:00Z"}},
			"2.0.0": {{Name: "legacy", LastUpdateAt: "2026-01-01T10:00:00Z"}},
		},
	)

	list, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "2.0.0", "ios", false)

	require.NoError(t, err)
	assert.Equal(t, []types.SurfableBranch{{Name: "legacy", LastUpdateAt: "2026-01-01T10:00:00Z"}}, list.Branches)
}

func TestListSurfableBranchesRefusesDisabledChannel(t *testing.T) {
	service, _ := surfingService(
		t,
		map[string]*types.BranchSurfing{"production": {Enabled: false, Pattern: "*"}},
		map[string][]types.SurfableBranch{"3.0.0": {{Name: "pr-482", LastUpdateAt: "2026-08-01T10:00:00Z"}}},
	)

	_, err := service.ListSurfableBranches(context.Background(), surfingAppID, "production", "3.0.0", "ios", false)

	var protocolErr *ExpoProtocolError
	require.ErrorAs(t, err, &protocolErr)
	assert.Equal(t, http.StatusNotFound, protocolErr.StatusCode)
}

// An unknown channel answers exactly as a disabled one does, so the endpoint
// cannot be used to enumerate which channels exist.
func TestListSurfableBranchesRefusesUnknownChannelIdentically(t *testing.T) {
	service, _ := surfingService(
		t,
		map[string]*types.BranchSurfing{"production": {Enabled: false, Pattern: "*"}},
		nil,
	)

	_, disabledErr := service.ListSurfableBranches(context.Background(), surfingAppID, "production", "3.0.0", "ios", false)
	_, unknownErr := service.ListSurfableBranches(context.Background(), surfingAppID, "nope", "3.0.0", "ios", false)

	// Comparing Error() alone compares only the message: ExpoProtocolError.Error()
	// returns Message and nothing else, so a 403 on one arm and a 404 on the other
	// would pass while handing back the very oracle this test exists to close.
	var disabled, unknown *ExpoProtocolError
	require.ErrorAs(t, disabledErr, &disabled)
	require.ErrorAs(t, unknownErr, &unknown)
	assert.Equal(t, disabled.StatusCode, unknown.StatusCode)
	assert.Equal(t, http.StatusNotFound, unknown.StatusCode)
	assert.Equal(t, disabled.Message, unknown.Message)
}

func TestListSurfableBranchesRejectsInvalidChannelName(t *testing.T) {
	service, _ := surfingService(t, nil, nil)

	_, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa/../prod", "3.0.0", "ios", false)

	require.Error(t, err)
	assert.True(t, validation.IsValidationError(err), "a malformed name is a validation error, not a 404")
}

func TestSetBranchSurfingCollapsesWildcards(t *testing.T) {
	service, channelRepo := surfingService(t, map[string]*types.BranchSurfing{"qa": {}}, nil)

	err := service.SetBranchSurfing(context.Background(), surfingAppID, "qa", types.BranchSurfing{
		Enabled: true,
		Pattern: "pr-**",
	})

	require.NoError(t, err)
	assert.Equal(t, types.BranchSurfing{Enabled: true, Pattern: "pr-*"}, channelRepo.surfingWrites["qa"])
}

func TestSetBranchSurfingRejectsInvalidPattern(t *testing.T) {
	service, channelRepo := surfingService(t, map[string]*types.BranchSurfing{"qa": {}}, nil)

	err := service.SetBranchSurfing(context.Background(), surfingAppID, "qa", types.BranchSurfing{
		Enabled: true,
		Pattern: "",
	})

	require.Error(t, err)
	assert.Empty(t, channelRepo.surfingWrites)
}

// Every launch of every build carrying the picker calls this endpoint, so a
// second reader must not reach the database. This is what makes the client's
// boot probe affordable at fleet scale.
func TestListSurfableBranchesIsAnsweredFromCache(t *testing.T) {
	channelRepo := &countingChannelRepo{
		fakeChannelRepo: &fakeChannelRepo{surfing: map[string]*types.BranchSurfing{
			"qa": {Enabled: true, Pattern: "*"},
		}},
	}
	branchRepo := &countingBranchRepo{surfable: map[string][]types.SurfableBranch{
		"3.0.0": {{Name: "pr-482", LastUpdateAt: "2026-08-01T10:00:00Z"}},
	}}
	t.Setenv("DB_URL", "postgres://stub")
	ForgetBranchSurfing(surfingAppID, "qa")
	ForgetSurfableBranches(surfingAppID, "3.0.0", "ios")
	service := NewChannelService(branchRepo, channelRepo)

	for i := 0; i < 5; i++ {
		_, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", "ios", false)
		require.NoError(t, err)
	}

	assert.Equal(t, 1, channelRepo.surfingReads, "the channel setting must be read once")
	assert.Equal(t, 1, branchRepo.reads, "the branch list must be read once")
}

type countingBranchRepo struct {
	fakeBranchRepo
	surfable map[string][]types.SurfableBranch
	reads    int
}

func (r *countingBranchRepo) GetSurfableBranches(_ context.Context, _, runtimeVersion string, _ types.Platform) ([]types.SurfableBranch, error) {
	r.reads++
	return r.surfable[runtimeVersion], nil
}

// The cap lands AFTER the pattern filter: a narrow pattern whose matches sit
// past the fiftieth newest branch must still find them, or a busy monorepo
// would show teams an empty list while their branches exist.
func TestListSurfableBranchesCapsAfterTheFilter(t *testing.T) {
	all := make([]types.SurfableBranch, 0, 120)
	for i := 0; i < 120; i++ {
		// Newest first, like the SQL. The pattern targets ONLY the oldest 20.
		name := fmt.Sprintf("pr-%d", i)
		if i >= 100 {
			name = fmt.Sprintf("team-%d", i)
		}
		all = append(all, types.SurfableBranch{Name: name, LastUpdateAt: "2026-08-01T10:00:00Z"})
	}

	wide, _ := surfingService(t,
		map[string]*types.BranchSurfing{"qa": {Enabled: true, Pattern: "*"}},
		map[string][]types.SurfableBranch{"3.0.0": all})
	list, err := wide.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", "ios", false)
	require.NoError(t, err)
	assert.Len(t, list.Branches, maxSurfableBranches)
	assert.Equal(t, "pr-0", list.Branches[0].Name, "newest first survives the cap")
	assert.Equal(t, 120, list.Total, "total counts every match, not just the page")

	narrow, _ := surfingService(t,
		map[string]*types.BranchSurfing{"qa": {Enabled: true, Pattern: "team-*"}},
		map[string][]types.SurfableBranch{"3.0.0": all})
	teamList, err := narrow.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", "ios", false)
	require.NoError(t, err)
	// Cap-before-filter would return nothing at all here: the ten newest branches
	// are all pr-*, so a page taken before the pattern ran would hold no match.
	assert.Len(t, teamList.Branches, maxSurfableBranches)
	assert.Equal(t, "team-100", teamList.Branches[0].Name)
	assert.Equal(t, 20, teamList.Total, "every match past the page is still counted")
}

// The first page is what every device downloads at launch; "see all" widens it
// without ever becoming unbounded. Total is the same either way, which is what
// lets a client know it is looking at part of the list.
func TestSeeAllWidensThePageWithoutUnboundingIt(t *testing.T) {
	all := make([]types.SurfableBranch, 0, 600)
	for i := 0; i < 600; i++ {
		all = append(all, types.SurfableBranch{
			Name:         fmt.Sprintf("pr-%d", i),
			LastUpdateAt: "2026-08-01T10:00:00Z",
		})
	}
	service, _ := surfingService(t,
		map[string]*types.BranchSurfing{"qa": {Enabled: true, Pattern: "pr-*"}},
		map[string][]types.SurfableBranch{"3.0.0": all})

	page, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", "ios", false)
	require.NoError(t, err)
	assert.Len(t, page.Branches, maxSurfableBranches)
	assert.Equal(t, 600, page.Total, "the count is of every match, so a client can say what it is missing")

	everything, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", "ios", true)
	require.NoError(t, err)
	assert.Len(t, everything.Branches, maxAllSurfableBranches, "see all is wider, not unbounded")
	assert.Equal(t, 600, everything.Total)
}

// The setting is cached under the channel NAME. Deleting the channel must drop
// it, or the channel keeps answering after it is gone — and a channel recreated
// under the same name starts out holding a permission nobody granted it.
func TestDeletingAChannelDropsItsSurfingPermission(t *testing.T) {
	service, channelRepo := surfingService(t,
		map[string]*types.BranchSurfing{"qa": {Enabled: true, Pattern: "pr-*"}},
		map[string][]types.SurfableBranch{"3.0.0": {{Name: "pr-1", LastUpdateAt: "2026-08-01T10:00:00Z"}}})

	_, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", "ios", false)
	require.NoError(t, err, "the setting is now cached")

	require.NoError(t, service.DeleteChannel(context.Background(), "qa", surfingAppID))
	channelRepo.surfing = map[string]*types.BranchSurfing{}

	_, err = service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", "ios", false)
	assert.Error(t, err, "a deleted channel must not keep answering from cache")
}

// The channel's own branch is deliberately unselectable: asking for it is treated
// as asking for nothing, so that a device in a progressive rollout keeps being
// drawn by the rollout rather than pinned by its own request. Listing it would
// therefore offer a switch the server will not honour — the tester taps it and
// nothing happens. Going back is the client's reset, not an entry in this list.
func TestTheChannelsOwnBranchIsNotOfferedAsASwitch(t *testing.T) {
	service, channelRepo := surfingService(t,
		map[string]*types.BranchSurfing{"qa": {Enabled: true, Pattern: "*"}},
		map[string][]types.SurfableBranch{"3.0.0": {
			{Name: "pr-1", LastUpdateAt: "2026-08-02T10:00:00Z"},
			{Name: "staging", LastUpdateAt: "2026-08-01T10:00:00Z"},
		}})
	channelRepo.mappings = map[string]*expo.ChannelMapping{
		"qa": {BranchName: "staging"},
	}

	list, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", "ios", false)

	require.NoError(t, err)
	assert.Equal(t, []types.SurfableBranch{{Name: "pr-1", LastUpdateAt: "2026-08-02T10:00:00Z"}}, list.Branches)
	assert.Equal(t, 1, list.Total, "the excluded branch must not be counted either, or the client offers a see-all that finds nothing")
}

// A channel that does not exist must be cached exactly like one with surfing
// off. Leaving it uncached made the two observably different: the disabled
// channel answered from memory, the unknown one hit Postgres. An unauthenticated
// caller could time the difference to learn which channel names exist, and drive
// one uncached query per request while doing it.
func TestAnUnknownChannelIsCachedLikeADisabledOne(t *testing.T) {
	channelRepo := &countingChannelRepo{
		fakeChannelRepo: &fakeChannelRepo{surfing: map[string]*types.BranchSurfing{
			"production": {Enabled: false, Pattern: "*"},
		}},
	}
	t.Setenv("DB_URL", "postgres://stub")
	ForgetBranchSurfing(surfingAppID, "production")
	ForgetBranchSurfing(surfingAppID, "nope")
	service := NewChannelService(fakeBranchRepo{}, channelRepo)

	for i := 0; i < 3; i++ {
		_, unknownErr := service.ListSurfableBranches(context.Background(), surfingAppID, "nope", "3.0.0", "ios", false)
		_, disabledErr := service.ListSurfableBranches(context.Background(), surfingAppID, "production", "3.0.0", "ios", false)
		require.Error(t, unknownErr)
		require.Error(t, disabledErr)
	}

	assert.Equal(t, 2, channelRepo.surfingReads,
		"one read each, then both answered from cache — an unknown channel that kept reading is a timing oracle")
}

// The flip side of caching the negative: a channel created under a name someone
// already asked for must not stay invisible for the rest of the TTL.
func TestCreatingAChannelClearsTheCachedNegative(t *testing.T) {
	service, channelRepo := surfingService(t,
		map[string]*types.BranchSurfing{},
		map[string][]types.SurfableBranch{"3.0.0": {{Name: "pr-1", LastUpdateAt: "2026-08-01T10:00:00Z"}}})
	_ = channelRepo

	_, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", "ios", false)
	require.Error(t, err, "unknown channel, now cached as such")

	_, err = service.CreateChannel(context.Background(), surfingAppID, nil, "qa")
	require.NoError(t, err)
	channelRepo.surfing = map[string]*types.BranchSurfing{"qa": {Enabled: true, Pattern: "pr-*"}}

	list, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", "ios", false)
	require.NoError(t, err, "the cached 'no such channel' must not outlive the channel's creation")
	assert.Len(t, list.Branches, 1)
}

// The mapped-branch exclusion depends on a read that can fail. Swallowing that
// error leaves mappedBranch empty, which turns the filter into a no-op and puts
// the unselectable branch back in the picker — silently undoing the fix, on a
// path nobody would think to check.
func TestAMappingReadFailureIsNotSwallowed(t *testing.T) {
	channelRepo := &mappingErrorChannelRepo{
		fakeChannelRepo: &fakeChannelRepo{surfing: map[string]*types.BranchSurfing{
			"qa": {Enabled: true, Pattern: "*"},
		}},
	}
	t.Setenv("DB_URL", "postgres://stub")
	ForgetBranchSurfing(surfingAppID, "qa")
	ForgetSurfableBranches(surfingAppID, "3.0.0", "ios")
	cache2.GetCache().Delete(channelMappingCacheKey(surfingAppID, "qa"))
	service := NewChannelService(
		fakeBranchRepo{surfable: map[string][]types.SurfableBranch{
			"3.0.0": {{Name: "staging", LastUpdateAt: "2026-08-01T10:00:00Z"}},
		}},
		channelRepo)

	_, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", "ios", false)

	assert.Error(t, err, "a list that cannot be filtered must fail rather than come back wrong")
}

type mappingErrorChannelRepo struct{ *fakeChannelRepo }

func (mappingErrorChannelRepo) GetChannelBranchMapping(_ context.Context, _, _ string) (*expo.ChannelMapping, error) {
	return nil, assert.AnError
}
