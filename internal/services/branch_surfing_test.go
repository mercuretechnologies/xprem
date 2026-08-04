package services

import (
	"context"
	"fmt"
	"net/http"
	"testing"
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
		ForgetSurfableBranches(surfingAppID, runtimeVersion)
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

	list, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", false)

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

	list, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "2.0.0", false)

	require.NoError(t, err)
	assert.Equal(t, []types.SurfableBranch{{Name: "legacy", LastUpdateAt: "2026-01-01T10:00:00Z"}}, list.Branches)
}

func TestListSurfableBranchesRefusesDisabledChannel(t *testing.T) {
	service, _ := surfingService(
		t,
		map[string]*types.BranchSurfing{"production": {Enabled: false, Pattern: "*"}},
		map[string][]types.SurfableBranch{"3.0.0": {{Name: "pr-482", LastUpdateAt: "2026-08-01T10:00:00Z"}}},
	)

	_, err := service.ListSurfableBranches(context.Background(), surfingAppID, "production", "3.0.0", false)

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

	_, disabledErr := service.ListSurfableBranches(context.Background(), surfingAppID, "production", "3.0.0", false)
	_, unknownErr := service.ListSurfableBranches(context.Background(), surfingAppID, "nope", "3.0.0", false)

	require.Error(t, disabledErr)
	require.Error(t, unknownErr)
	assert.Equal(t, disabledErr.Error(), unknownErr.Error())
}

func TestListSurfableBranchesRejectsInvalidChannelName(t *testing.T) {
	service, _ := surfingService(t, nil, nil)

	_, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa/../prod", "3.0.0", false)

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
	ForgetSurfableBranches(surfingAppID, "3.0.0")
	service := NewChannelService(branchRepo, channelRepo)

	for i := 0; i < 5; i++ {
		_, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", false)
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

func (r *countingBranchRepo) GetSurfableBranches(_ context.Context, _, runtimeVersion string) ([]types.SurfableBranch, error) {
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
	list, err := wide.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", false)
	require.NoError(t, err)
	assert.Len(t, list.Branches, maxSurfableBranches)
	assert.Equal(t, "pr-0", list.Branches[0].Name, "newest first survives the cap")
	assert.Equal(t, 120, list.Total, "total counts every match, not just the page")

	narrow, _ := surfingService(t,
		map[string]*types.BranchSurfing{"qa": {Enabled: true, Pattern: "team-*"}},
		map[string][]types.SurfableBranch{"3.0.0": all})
	teamList, err := narrow.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", false)
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

	page, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", false)
	require.NoError(t, err)
	assert.Len(t, page.Branches, maxSurfableBranches)
	assert.Equal(t, 600, page.Total, "the count is of every match, so a client can say what it is missing")

	everything, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0", true)
	require.NoError(t, err)
	assert.Len(t, everything.Branches, maxAllSurfableBranches, "see all is wider, not unbounded")
	assert.Equal(t, 600, everything.Total)
}
