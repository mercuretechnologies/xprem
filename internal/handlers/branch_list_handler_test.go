package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/providers/expo"
	"xprem/internal/services"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const surfingTestAppID = "1a2b3c4d-0000-0000-0000-000000000001"

type stubChannelRepo struct {
	surfing map[string]*types.BranchSurfing
}

func (r *stubChannelRepo) InsertChannel(_ context.Context, _ string, _ *int64, _ string) (int64, error) {
	return 0, nil
}
func (r *stubChannelRepo) DeleteChannel(_ context.Context, _, _ string) error { return nil }
func (r *stubChannelRepo) GetChannelNameByBranchName(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}
func (r *stubChannelRepo) GetChannels(_ context.Context, _ string) ([]types.ChannelMapping, error) {
	return nil, nil
}
func (r *stubChannelRepo) GetChannelBranchMapping(_ context.Context, _, _ string) (*expo.ChannelMapping, error) {
	return nil, nil
}
func (r *stubChannelRepo) GetBranchSurfing(_ context.Context, _, channelName string) (*types.BranchSurfing, error) {
	return r.surfing[channelName], nil
}
func (r *stubChannelRepo) SetBranchSurfing(_ context.Context, _, _ string, _ types.BranchSurfing) error {
	return nil
}

type stubBranchRepo struct {
	surfable map[string][]types.SurfableBranch
}

func (r *stubBranchRepo) InsertBranch(_ context.Context, _ pgdb.InsertBranchParams) (int64, error) {
	return 0, nil
}
func (r *stubBranchRepo) UpsertBranchAndRuntimeVersion(_ context.Context, _, _, _ string) error {
	return nil
}
func (r *stubBranchRepo) GetUpdatedMetadataByBranchName(_ context.Context, _, _ string) ([]pgdb.GetUpdatesMetadataByBranchNameRow, error) {
	return nil, nil
}
func (r *stubBranchRepo) DeleteBranchByName(_ context.Context, _, _ string) error { return nil }
func (r *stubBranchRepo) GetBranches(_ context.Context, _ string) ([]types.BranchMapping, error) {
	return nil, nil
}
func (r *stubBranchRepo) GetSurfableBranches(_ context.Context, _, runtimeVersion string) ([]types.SurfableBranch, error) {
	return r.surfable[runtimeVersion], nil
}
func (r *stubBranchRepo) GetRuntimeVersionsWithUpdateStats(_ context.Context, _, _ string) ([]types.RuntimeVersionWithStats, error) {
	return nil, nil
}
func (r *stubBranchRepo) UpdateChannelBranchMapping(_ context.Context, _, _, _ string) error {
	return nil
}
func (r *stubBranchRepo) CreateRuntimeVersion(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}
func (r *stubBranchRepo) GetBranchByName(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}

func surfingHandler() *BranchListHandler {
	channelRepo := &stubChannelRepo{surfing: map[string]*types.BranchSurfing{
		"qa":         {Enabled: true, Pattern: "pr-*"},
		"production": {Enabled: false, Pattern: "*"},
	}}
	branchRepo := &stubBranchRepo{surfable: map[string][]types.SurfableBranch{
		"3.0.0": {
			{Name: "pr-482", LastUpdateAt: "2026-08-01T10:00:00Z"},
			{Name: "production", LastUpdateAt: "2026-07-30T10:00:00Z"},
		},
	}}
	return NewBranchListHandler(services.NewChannelService(branchRepo, channelRepo))
}

func branchListRequest(target string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	return r
}

func TestBranchListRejectsMissingHeaders(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"no app id", map[string]string{"expo-channel-name": "qa", "expo-runtime-version": "3.0.0"}},
		{"no channel", map[string]string{"expo-app-id": surfingTestAppID, "expo-runtime-version": "3.0.0"}},
		{"no runtime version", map[string]string{"expo-app-id": surfingTestAppID, "expo-channel-name": "qa"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			surfingHandler().HandleBranchList(w, branchListRequest("/branch_lists", tc.headers))
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestBranchListServesBranchesMatchingThePattern(t *testing.T) {
	w := httptest.NewRecorder()
	surfingHandler().HandleBranchList(w, branchListRequest("/branch_lists", map[string]string{
		"expo-app-id":          surfingTestAppID,
		"expo-channel-name":    "qa",
		"expo-runtime-version": "3.0.0",
	}))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "private, max-age=0", w.Header().Get("cache-control"))
	var branches []types.SurfableBranch
	require.NoError(t, json.NewDecoder(w.Body).Decode(&branches))
	assert.Equal(t, []types.SurfableBranch{{Name: "pr-482", LastUpdateAt: "2026-08-01T10:00:00Z"}}, branches)
}

func TestBranchListRefusesChannelWithSurfingOff(t *testing.T) {
	w := httptest.NewRecorder()
	surfingHandler().HandleBranchList(w, branchListRequest("/branch_lists", map[string]string{
		"expo-app-id":          surfingTestAppID,
		"expo-channel-name":    "production",
		"expo-runtime-version": "3.0.0",
	}))

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// The runtime version falls back to the query string, as the manifest route
// does for clients that cannot set the header.
func TestBranchListAcceptsRuntimeVersionFromQuery(t *testing.T) {
	w := httptest.NewRecorder()
	surfingHandler().HandleBranchList(w, branchListRequest("/branch_lists?runtimeVersion=3.0.0", map[string]string{
		"expo-app-id":       surfingTestAppID,
		"expo-channel-name": "qa",
	}))

	require.Equal(t, http.StatusOK, w.Code)
	var branches []types.SurfableBranch
	require.NoError(t, json.NewDecoder(w.Body).Decode(&branches))
	assert.Len(t, branches, 1)
}

// An unknown runtime version is an empty list, not an error: the channel allows
// surfing, there is simply nothing this binary can be served.
func TestBranchListIsEmptyForUnknownRuntimeVersion(t *testing.T) {
	w := httptest.NewRecorder()
	surfingHandler().HandleBranchList(w, branchListRequest("/branch_lists", map[string]string{
		"expo-app-id":          surfingTestAppID,
		"expo-channel-name":    "qa",
		"expo-runtime-version": "9.9.9",
	}))

	require.Equal(t, http.StatusOK, w.Code)
	var branches []types.SurfableBranch
	require.NoError(t, json.NewDecoder(w.Body).Decode(&branches))
	assert.Empty(t, branches)
}
