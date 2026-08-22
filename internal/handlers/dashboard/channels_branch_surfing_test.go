package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/providers/expo"
	"xprem/internal/services"
	"xprem/internal/types"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type surfingChannelRepo struct {
	written []types.BranchSurfing
}

func (r *surfingChannelRepo) InsertChannel(_ context.Context, _ string, _ *int64, _ string) (int64, error) {
	return 0, nil
}
func (r *surfingChannelRepo) DeleteChannel(_ context.Context, _, _ string) error { return nil }
func (r *surfingChannelRepo) GetChannelNameByBranchName(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}
func (r *surfingChannelRepo) GetChannels(_ context.Context, _ string) ([]types.ChannelMapping, error) {
	return nil, nil
}
func (r *surfingChannelRepo) GetChannelBranchMapping(_ context.Context, _, _ string) (*expo.ChannelMapping, error) {
	return nil, nil
}
func (r *surfingChannelRepo) GetBranchSurfing(_ context.Context, _, _ string) (*types.BranchSurfing, error) {
	return &types.BranchSurfing{}, nil
}
func (r *surfingChannelRepo) SetBranchSurfing(_ context.Context, _, _ string, surfing types.BranchSurfing) error {
	r.written = append(r.written, surfing)
	return nil
}

type surfingBranchRepo struct{}

func (surfingBranchRepo) InsertBranch(_ context.Context, _ pgdb.InsertBranchParams) (int64, error) {
	return 0, nil
}
func (surfingBranchRepo) UpsertBranchAndRuntimeVersion(_ context.Context, _, _, _ string) error {
	return nil
}
func (surfingBranchRepo) GetUpdatedMetadataByBranchName(_ context.Context, _, _ string) ([]pgdb.GetUpdatesMetadataByBranchNameRow, error) {
	return nil, nil
}
func (surfingBranchRepo) DeleteBranchByName(_ context.Context, _, _ string) error { return nil }
func (surfingBranchRepo) GetBranches(_ context.Context, _ string) ([]types.BranchMapping, error) {
	return nil, nil
}
func (surfingBranchRepo) GetSurfableBranches(_ context.Context, _, _ string, _ types.Platform) ([]types.SurfableBranch, error) {
	return nil, nil
}
func (surfingBranchRepo) GetRuntimeVersionsWithUpdateStats(_ context.Context, _, _ string) ([]types.RuntimeVersionWithStats, error) {
	return nil, nil
}
func (surfingBranchRepo) UpdateChannelBranchMapping(_ context.Context, _, _, _ string) error {
	return nil
}
func (surfingBranchRepo) CreateRuntimeVersion(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}
func (surfingBranchRepo) GetBranchByName(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}

func setBranchSurfing(t *testing.T, body string) (*httptest.ResponseRecorder, *surfingChannelRepo) {
	t.Helper()
	channelRepo := &surfingChannelRepo{}
	handler := NewChannelHandler(services.NewChannelService(surfingBranchRepo{}, channelRepo))

	r := httptest.NewRequest(http.MethodPut, "/channels/qa/branch-surfing", strings.NewReader(body))
	r = mux.SetURLVars(r, map[string]string{"APP_ID": "app-1", "CHANNEL": "qa"})
	w := httptest.NewRecorder()
	handler.SetBranchSurfingHandler(w, r)
	return w, channelRepo
}

func TestSetBranchSurfingStoresPattern(t *testing.T) {
	w, channelRepo := setBranchSurfing(t, `{"enabled":true,"pattern":"pr-*"}`)

	require.Equal(t, http.StatusNoContent, w.Code)
	require.Len(t, channelRepo.written, 1)
	assert.Equal(t, types.BranchSurfing{Enabled: true, Pattern: "pr-*"}, channelRepo.written[0])
}

// A PUT replaces the whole setting, so an omitted pattern is refused rather
// than filled in: defaulting it would silently widen a narrower channel.
func TestSetBranchSurfingRefusesMissingPattern(t *testing.T) {
	w, channelRepo := setBranchSurfing(t, `{"enabled":true}`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, channelRepo.written)
}

func TestSetBranchSurfingRejectsMalformedPattern(t *testing.T) {
	w, channelRepo := setBranchSurfing(t, `{"enabled":true,"pattern":"pr/../prod"}`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, channelRepo.written)
}

func TestSetBranchSurfingRejectsInvalidBody(t *testing.T) {
	w, channelRepo := setBranchSurfing(t, `not json`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, channelRepo.written)
}
