package services

import (
	"context"
	"testing"
	"xprem/internal/cache"
	"xprem/internal/providers/expo"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
)

const guardAppID = "asset-branch-guard-test"

// A nil setting means the channel does not exist, so its key is absent rather
// than present and nil.
func guardService(t *testing.T, surfing *types.BranchSurfing) *ExpoProtocolService {
	t.Helper()
	t.Setenv("DB_URL", "postgres://stub")
	cache.GetCache().Delete(channelBranchSurfingCacheKey(guardAppID, "qa"))
	settings := map[string]*types.BranchSurfing{}
	if surfing != nil {
		settings["qa"] = surfing
	}
	return NewExpoProtocolService(fakeAppRepo{}, &fakeChannelRepo{surfing: settings}, nil, nil, nil)
}

func TestAssetBranchGuardMirrorsTheManifest(t *testing.T) {
	mapped := &expo.ChannelMapping{Id: "1", BranchName: "staging"}
	withRollout := &expo.ChannelMapping{
		Id: "1", BranchName: "staging",
		Rollout: &expo.ChannelRolloutInfo{ID: "r1", BranchName: "canary", Percentage: 50},
	}

	cases := []struct {
		name    string
		surfing *types.BranchSurfing
		mapping *expo.ChannelMapping
		branch  string
		want    bool
	}{
		{"mapped branch", &types.BranchSurfing{}, mapped, "staging", true},
		{"rollout branch", &types.BranchSurfing{}, withRollout, "canary", true},
		{"other branch, surfing off", &types.BranchSurfing{}, mapped, "pr-482", false},
		{"surfed branch matching the pattern", &types.BranchSurfing{Enabled: true, Pattern: "pr-*"}, mapped, "pr-482", true},
		{"branch outside the pattern", &types.BranchSurfing{Enabled: true, Pattern: "pr-*"}, mapped, "secret", false},
		{"wildcard exposes everything", &types.BranchSurfing{Enabled: true, Pattern: "*"}, mapped, "secret", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := guardService(t, tc.surfing)
			allowed := service.isAssetBranchAllowed(context.Background(), guardAppID, "qa", tc.branch, tc.mapping)
			assert.Equal(t, tc.want, allowed)
		})
	}
}

// An unknown channel grants nothing, so a crafted URL naming one cannot read a
// branch it has no claim to.
func TestAssetBranchGuardDeniesOnUnknownChannel(t *testing.T) {
	service := guardService(t, nil)
	mapped := &expo.ChannelMapping{Id: "1", BranchName: "staging"}

	assert.False(t, service.isAssetBranchAllowed(context.Background(), guardAppID, "qa", "pr-482", mapped))
}
