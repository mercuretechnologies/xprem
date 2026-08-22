package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
func (r *stubChannelRepo) GetChannelBranchMapping(_ context.Context, _, _ string) (*expo.ChannelResolution, error) {
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

func (r *stubBranchRepo) InsertBranch(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}
func (r *stubBranchRepo) UpsertBranchAndRuntimeVersion(_ context.Context, _, _, _ string) error {
	return nil
}
func (r *stubBranchRepo) GetUpdatedMetadataByBranchName(_ context.Context, _, _ string) ([]types.UpdateRef, error) {
	return nil, nil
}
func (r *stubBranchRepo) DeleteBranchByName(_ context.Context, _, _ string) error { return nil }
func (r *stubBranchRepo) GetBranches(_ context.Context, _ string) ([]types.BranchMapping, error) {
	return nil, nil
}
func (r *stubBranchRepo) GetSurfableBranches(_ context.Context, _, runtimeVersion string, _ string) ([]types.SurfableBranch, error) {
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

// The delivery-path caches are process-wide singletons keyed on the app id, so a
// test that shares one with its neighbours would read their answers. Surfing also
// only exists on the control plane, so DB_URL has to look set.
func surfingHandler(t *testing.T) *BranchListHandler {
	t.Helper()
	t.Setenv("DB_URL", "postgres://stub")
	services.ForgetBranchSurfing(surfingTestAppID, "qa")
	services.ForgetBranchSurfing(surfingTestAppID, "production")
	services.ForgetSurfableBranches(surfingTestAppID, "3.0.0", "ios")
	services.ForgetSurfableBranches(surfingTestAppID, "9.9.9", "ios")
	services.ForgetSurfableBranches(surfingTestAppID, "2.0.0", "ios")
	return newSurfingHandler()
}

// Same isolation, with a branch set the test chooses.
func surfingHandlerWithBranches(t *testing.T, branches []types.SurfableBranch) *BranchListHandler {
	t.Helper()
	t.Setenv("DB_URL", "postgres://stub")
	services.ForgetBranchSurfing(surfingTestAppID, "qa")
	services.ForgetSurfableBranches(surfingTestAppID, "3.0.0", "ios")
	channelRepo := &stubChannelRepo{surfing: map[string]*types.BranchSurfing{
		"qa": {Enabled: true, Pattern: "pr-*"},
	}}
	branchRepo := &stubBranchRepo{surfable: map[string][]types.SurfableBranch{"3.0.0": branches}}
	return NewBranchListHandler(services.NewChannelService(branchRepo, channelRepo))
}

func newSurfingHandler() *BranchListHandler {
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
			surfingHandler(t).HandleBranchList(w, branchListRequest("/branch_lists", tc.headers))
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestBranchListServesBranchesMatchingThePattern(t *testing.T) {
	w := httptest.NewRecorder()
	surfingHandler(t).HandleBranchList(w, branchListRequest("/branch_lists", map[string]string{
		"expo-app-id":          surfingTestAppID,
		"expo-channel-name":    "qa",
		"expo-runtime-version": "3.0.0",
		"expo-platform":        "ios",
	}))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "private, max-age=0", w.Header().Get("cache-control"))
	var list types.SurfableBranchList
	require.NoError(t, json.NewDecoder(w.Body).Decode(&list))
	assert.Equal(t, []types.SurfableBranch{{Name: "pr-482", LastUpdateAt: "2026-08-01T10:00:00Z"}}, list.Branches)
	assert.Equal(t, 1, list.Total)
}

func TestBranchListRefusesChannelWithSurfingOff(t *testing.T) {
	w := httptest.NewRecorder()
	surfingHandler(t).HandleBranchList(w, branchListRequest("/branch_lists", map[string]string{
		"expo-app-id":          surfingTestAppID,
		"expo-channel-name":    "production",
		"expo-runtime-version": "3.0.0",
		"expo-platform":        "ios",
	}))

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// The runtime version falls back to the query string, as the manifest route
// does for clients that cannot set the header.
func TestBranchListAcceptsRuntimeVersionFromQuery(t *testing.T) {
	w := httptest.NewRecorder()
	surfingHandler(t).HandleBranchList(w, branchListRequest("/branch_lists?runtimeVersion=3.0.0", map[string]string{
		"expo-app-id":       surfingTestAppID,
		"expo-channel-name": "qa",
		"expo-platform":     "ios",
	}))

	require.Equal(t, http.StatusOK, w.Code)
	var list types.SurfableBranchList
	require.NoError(t, json.NewDecoder(w.Body).Decode(&list))
	assert.Len(t, list.Branches, 1)
}

// An unknown runtime version is an empty list, not an error: the channel allows
// surfing, there is simply nothing this binary can be served.
func TestBranchListIsEmptyForUnknownRuntimeVersion(t *testing.T) {
	w := httptest.NewRecorder()
	surfingHandler(t).HandleBranchList(w, branchListRequest("/branch_lists", map[string]string{
		"expo-app-id":          surfingTestAppID,
		"expo-channel-name":    "qa",
		"expo-runtime-version": "9.9.9",
		"expo-platform":        "ios",
	}))

	require.Equal(t, http.StatusOK, w.Code)
	var list types.SurfableBranchList
	require.NoError(t, json.NewDecoder(w.Body).Decode(&list))
	assert.Empty(t, list.Branches)
	assert.Equal(t, 0, list.Total)
}

// all=1 is the only way to get past the first page, so a device that does not
// ask for it can never be handed the wide answer by accident.
func TestBranchListPageWidensOnlyWithTheAllFlag(t *testing.T) {
	branches := make([]types.SurfableBranch, 0, 30)
	for i := 0; i < 30; i++ {
		branches = append(branches, types.SurfableBranch{
			Name:         fmt.Sprintf("pr-%d", i),
			LastUpdateAt: "2026-08-01T10:00:00Z",
		})
	}
	handler := surfingHandlerWithBranches(t, branches)

	read := func(url string) types.SurfableBranchList {
		w := httptest.NewRecorder()
		handler.HandleBranchList(w, branchListRequest(url, map[string]string{
			"expo-app-id":          surfingTestAppID,
			"expo-channel-name":    "qa",
			"expo-runtime-version": "3.0.0",
			"expo-platform":        "ios",
		}))
		require.Equal(t, http.StatusOK, w.Code)
		var list types.SurfableBranchList
		require.NoError(t, json.NewDecoder(w.Body).Decode(&list))
		return list
	}

	first := read("/branch_lists")
	assert.Len(t, first.Branches, 10)
	assert.Equal(t, 30, first.Total)

	everything := read("/branch_lists?all=1")
	assert.Len(t, everything.Branches, 30)
	assert.Equal(t, 30, everything.Total)
}

// A branch whose only update is for the other platform cannot be served here:
// the manifest route filters on platform too, so offering it would hand the
// tester a switch that silently drops them back on the channel's branch.
func TestBranchListIsScopedToThePlatform(t *testing.T) {
	t.Setenv("DB_URL", "postgres://stub")
	services.ForgetBranchSurfing(surfingTestAppID, "qa")
	services.ForgetSurfableBranches(surfingTestAppID, "3.0.0", "ios")
	services.ForgetSurfableBranches(surfingTestAppID, "3.0.0", "android")
	channelRepo := &stubChannelRepo{surfing: map[string]*types.BranchSurfing{
		"qa": {Enabled: true, Pattern: "pr-*"},
	}}
	branchRepo := &platformBranchRepo{}
	handler := NewBranchListHandler(services.NewChannelService(branchRepo, channelRepo))

	for _, platform := range []string{"ios", "android"} {
		w := httptest.NewRecorder()
		handler.HandleBranchList(w, branchListRequest("/branch_lists", map[string]string{
			"expo-app-id":          surfingTestAppID,
			"expo-channel-name":    "qa",
			"expo-runtime-version": "3.0.0",
			"expo-platform":        platform,
		}))
		require.Equal(t, http.StatusOK, w.Code)
		var list types.SurfableBranchList
		require.NoError(t, json.NewDecoder(w.Body).Decode(&list))
		require.Len(t, list.Branches, 1)
		assert.Equal(t, "pr-"+platform, list.Branches[0].Name,
			"%s must not be offered the other platform's branch", platform)
	}
}

// Answers a different branch per platform, so a list built without it returns
// the wrong one rather than merely a longer one.
type platformBranchRepo struct{ services.BranchRepository }

func (platformBranchRepo) GetSurfableBranches(_ context.Context, _, _ string, platform string) ([]types.SurfableBranch, error) {
	return []types.SurfableBranch{{Name: "pr-" + platform, LastUpdateAt: "2026-08-01T10:00:00Z"}}, nil
}

func TestBranchListRejectsAMissingOrUnknownPlatform(t *testing.T) {
	for name, platform := range map[string]string{"absent": "", "unknown": "blackberry"} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			headers := map[string]string{
				"expo-app-id":          surfingTestAppID,
				"expo-channel-name":    "qa",
				"expo-runtime-version": "3.0.0",
			}
			if platform != "" {
				headers["expo-platform"] = platform
			}
			surfingHandler(t).HandleBranchList(w, branchListRequest("/branch_lists", headers))
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

// The answer is cached under the runtime version, empty answers included, and
// the local cache has no size ceiling — so an unbounded header is one cache
// entry per request for anyone who can reach this route, which is everyone.
func TestBranchListRejectsAnOversizedRuntimeVersion(t *testing.T) {
	w := httptest.NewRecorder()
	surfingHandler(t).HandleBranchList(w, branchListRequest("/branch_lists", map[string]string{
		"expo-app-id":          surfingTestAppID,
		"expo-channel-name":    "qa",
		"expo-runtime-version": strings.Repeat("9", maxRuntimeVersionLen+1),
		"expo-platform":        "ios",
	}))

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// A device already pinned to a branch only learns that surfing was switched off
// from this header: the panel hides itself on the 404, and after that there is
// no interface left to unpin from. It must be on BOTH refusals — a header only
// the disabled channel carried would tell an unauthenticated caller which
// channel names exist, which the identical bodies exist to prevent.
func TestTheRefusalTellsAPinnedDeviceToUnpin(t *testing.T) {
	for name, channelName := range map[string]string{
		"surfing disabled": "production",
		"unknown channel":  "ghost",
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			surfingHandler(t).HandleBranchList(w, branchListRequest("/branch_lists", map[string]string{
				"expo-app-id":          surfingTestAppID,
				"expo-channel-name":    channelName,
				"expo-runtime-version": "3.0.0",
				"expo-platform":        "ios",
			}))

			require.Equal(t, http.StatusNotFound, w.Code)
			assert.Equal(t, "off", w.Header().Get(SurfingDisabledHeader))
		})
	}
}

// The signal must not ride on refusals the client cannot act on: clearing a
// tester's branch because a header was malformed would be a silent surprise.
func TestABadRequestCarriesNoUnpinSignal(t *testing.T) {
	w := httptest.NewRecorder()
	surfingHandler(t).HandleBranchList(w, branchListRequest("/branch_lists", map[string]string{
		"expo-app-id":          surfingTestAppID,
		"expo-channel-name":    "qa",
		"expo-runtime-version": "3.0.0",
		"expo-platform":        "blackberry",
	}))

	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, w.Header().Get(SurfingDisabledHeader))
}
