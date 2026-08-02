package services

import (
	"context"
	"net/http"
	"testing"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const surfingAppID = "1a2b3c4d-0000-0000-0000-000000000001"

func surfingService(surfing map[string]*types.BranchSurfing, surfable map[string][]types.SurfableBranch) (*ChannelService, *fakeChannelRepo) {
	channelRepo := &fakeChannelRepo{surfing: surfing}
	return NewChannelService(fakeBranchRepo{surfable: surfable}, channelRepo), channelRepo
}

func TestListSurfableBranchesFiltersOnPattern(t *testing.T) {
	service, _ := surfingService(
		map[string]*types.BranchSurfing{"qa": {Enabled: true, Pattern: "pr-*"}},
		map[string][]types.SurfableBranch{"3.0.0": {
			{Name: "pr-482", LastUpdateAt: "2026-08-01T10:00:00Z"},
			{Name: "production", LastUpdateAt: "2026-07-30T10:00:00Z"},
			{Name: "pr-31", LastUpdateAt: "2026-07-29T10:00:00Z"},
		}},
	)

	branches, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "3.0.0")

	require.NoError(t, err)
	assert.Equal(t, []types.SurfableBranch{
		{Name: "pr-482", LastUpdateAt: "2026-08-01T10:00:00Z"},
		{Name: "pr-31", LastUpdateAt: "2026-07-29T10:00:00Z"},
	}, branches)
}

// The runtime version is what the repository is asked for: a branch holding no
// update for the device's runtime version is unreachable, so it must never be
// offered.
func TestListSurfableBranchesScopesToRuntimeVersion(t *testing.T) {
	service, _ := surfingService(
		map[string]*types.BranchSurfing{"qa": {Enabled: true, Pattern: "*"}},
		map[string][]types.SurfableBranch{
			"3.0.0": {{Name: "pr-482", LastUpdateAt: "2026-08-01T10:00:00Z"}},
			"2.0.0": {{Name: "legacy", LastUpdateAt: "2026-01-01T10:00:00Z"}},
		},
	)

	branches, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa", "2.0.0")

	require.NoError(t, err)
	assert.Equal(t, []types.SurfableBranch{{Name: "legacy", LastUpdateAt: "2026-01-01T10:00:00Z"}}, branches)
}

func TestListSurfableBranchesRefusesDisabledChannel(t *testing.T) {
	service, _ := surfingService(
		map[string]*types.BranchSurfing{"production": {Enabled: false, Pattern: "*"}},
		map[string][]types.SurfableBranch{"3.0.0": {{Name: "pr-482", LastUpdateAt: "2026-08-01T10:00:00Z"}}},
	)

	_, err := service.ListSurfableBranches(context.Background(), surfingAppID, "production", "3.0.0")

	var protocolErr *ExpoProtocolError
	require.ErrorAs(t, err, &protocolErr)
	assert.Equal(t, http.StatusNotFound, protocolErr.StatusCode)
}

// An unknown channel answers exactly as a disabled one does, so the endpoint
// cannot be used to enumerate which channels exist.
func TestListSurfableBranchesRefusesUnknownChannelIdentically(t *testing.T) {
	service, _ := surfingService(
		map[string]*types.BranchSurfing{"production": {Enabled: false, Pattern: "*"}},
		nil,
	)

	_, disabledErr := service.ListSurfableBranches(context.Background(), surfingAppID, "production", "3.0.0")
	_, unknownErr := service.ListSurfableBranches(context.Background(), surfingAppID, "nope", "3.0.0")

	require.Error(t, disabledErr)
	require.Error(t, unknownErr)
	assert.Equal(t, disabledErr.Error(), unknownErr.Error())
}

func TestListSurfableBranchesRejectsInvalidChannelName(t *testing.T) {
	service, _ := surfingService(nil, nil)

	_, err := service.ListSurfableBranches(context.Background(), surfingAppID, "qa/../prod", "3.0.0")

	require.Error(t, err)
	assert.True(t, validation.IsValidationError(err), "a malformed name is a validation error, not a 404")
}

func TestSetBranchSurfingCollapsesWildcards(t *testing.T) {
	service, channelRepo := surfingService(map[string]*types.BranchSurfing{"qa": {}}, nil)

	err := service.SetBranchSurfing(context.Background(), surfingAppID, "qa", types.BranchSurfing{
		Enabled: true,
		Pattern: "pr-**",
	})

	require.NoError(t, err)
	assert.Equal(t, types.BranchSurfing{Enabled: true, Pattern: "pr-*"}, channelRepo.surfingWrites["qa"])
}

func TestSetBranchSurfingRejectsInvalidPattern(t *testing.T) {
	service, channelRepo := surfingService(map[string]*types.BranchSurfing{"qa": {}}, nil)

	err := service.SetBranchSurfing(context.Background(), surfingAppID, "qa", types.BranchSurfing{
		Enabled: true,
		Pattern: "",
	})

	require.Error(t, err)
	assert.Empty(t, channelRepo.surfingWrites)
}
