package services

import (
	"context"
	"testing"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These cover the wiring the unit tests cannot see: ManifestRequestParams.XpremBranch
// reaching the rule chain and changing which branch is served. Drop any link of that
// chain and only these fail.
func newSurfHarness(t *testing.T, surfing *types.BranchSurfing, rollout *types.ChannelRolloutInfo) *rolloutTestHarness {
	t.Helper()
	t.Setenv("DB_URL", "postgres://stub")
	h := newRolloutTestHarness(t)
	h.channelRepo.mappings["qa"] = &types.ChannelResolution{Id: "1", BranchName: "staging", Rollout: rollout}
	h.channelRepo.surfing = map[string]*types.BranchSurfing{"qa": surfing}
	h.seed(seedRow{branch: "staging", rtv: "1", platform: "ios", id: 100, checked: true})
	return h
}

func surfParams(h *rolloutTestHarness, requested string) ManifestRequestParams {
	return ManifestRequestParams{
		RequestID:       "test",
		AppID:           h.appId,
		ChannelName:     "qa",
		Platform:        "ios",
		RuntimeVersion:  "1",
		ProtocolVersion: 1,
		ClientID:        "device-1",
		XpremBranch:     requested,
	}
}

func TestResolveUpdateForDeviceServesTheSurfedBranch(t *testing.T) {
	h := newSurfHarness(t, &types.BranchSurfing{Enabled: true, Pattern: "pr-*"}, nil)
	h.seed(seedRow{branch: "pr-482", rtv: "1", platform: "ios", id: 200, checked: true})

	result, err := h.protocolService.ResolveUpdateForDevice(context.Background(), surfParams(h, "pr-482"))

	require.NoError(t, err)
	require.NotNil(t, result.Update)
	assert.Equal(t, "pr-482", result.BranchName)
	assert.Equal(t, "pr-482", result.Update.Branch)
}

func TestResolveUpdateForDeviceIgnoresTheHeaderWhenSurfingIsOff(t *testing.T) {
	h := newSurfHarness(t, &types.BranchSurfing{Enabled: false, Pattern: "*"}, nil)
	h.seed(seedRow{branch: "pr-482", rtv: "1", platform: "ios", id: 200, checked: true})

	result, err := h.protocolService.ResolveUpdateForDevice(context.Background(), surfParams(h, "pr-482"))

	require.NoError(t, err)
	assert.Equal(t, "staging", result.BranchName)
}

func TestResolveUpdateForDeviceIgnoresABranchOutsideThePattern(t *testing.T) {
	h := newSurfHarness(t, &types.BranchSurfing{Enabled: true, Pattern: "pr-*"}, nil)
	h.seed(seedRow{branch: "secret", rtv: "1", platform: "ios", id: 200, checked: true})

	result, err := h.protocolService.ResolveUpdateForDevice(context.Background(), surfParams(h, "secret"))

	require.NoError(t, err)
	assert.Equal(t, "staging", result.BranchName)
}

// The mapped branch trails the surfed one, so a branch with nothing for the device's
// runtime version falls back to it rather than stranding the device. Deliberately the
// mapped branch and not whatever the rest of the chain would have picked: a device
// asking for a branch has no business being drawn into a rollout it did not ask for.
func TestResolveUpdateForDeviceFallsBackWhenTheSurfedBranchHasNothing(t *testing.T) {
	h := newSurfHarness(t, &types.BranchSurfing{Enabled: true, Pattern: "pr-*"}, nil)
	h.seed(seedRow{branch: "pr-482", rtv: "99", platform: "ios", id: 200, checked: true})

	result, err := h.protocolService.ResolveUpdateForDevice(context.Background(), surfParams(h, "pr-482"))

	require.NoError(t, err)
	require.NotNil(t, result.Update)
	assert.Equal(t, "staging", result.BranchName)
}

// End to end this time, not just the rule: an explicit surf is not re-drawn against
// the channel rollout, even for a device the rollout would have moved.
func TestResolveUpdateForDeviceSurfOutranksAnActiveRollout(t *testing.T) {
	const salt = "surf-vs-rollout-salt"
	h := newSurfHarness(t,
		&types.BranchSurfing{Enabled: true, Pattern: "pr-*"},
		&types.ChannelRolloutInfo{ID: salt, BranchName: "canary", Percentage: 100},
	)
	h.seed(seedRow{branch: "canary", rtv: "1", platform: "ios", id: 200, checked: true})
	h.seed(seedRow{branch: "pr-482", rtv: "1", platform: "ios", id: 300, checked: true})

	surfed, err := h.protocolService.ResolveUpdateForDevice(context.Background(), surfParams(h, "pr-482"))
	require.NoError(t, err)
	assert.Equal(t, "pr-482", surfed.BranchName)

	// Same channel, same device, no header: the rollout applies as usual.
	plain, err := h.protocolService.ResolveUpdateForDevice(context.Background(), surfParams(h, ""))
	require.NoError(t, err)
	assert.Equal(t, "canary", plain.BranchName)
}
