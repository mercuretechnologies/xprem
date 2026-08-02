package services

import (
	"context"
	"testing"
	"xprem/internal/providers/expo"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func surfRequest(requested string, surfing types.BranchSurfing, rollout *expo.ChannelRolloutInfo) *BranchResolutionRequest {
	return &BranchResolutionRequest{
		AppID:           "app-1",
		ChannelName:     "qa",
		ClientID:        "device-1",
		Platform:        "ios",
		RuntimeVersion:  "3.0.0",
		Mapping:         &expo.ChannelMapping{Id: "1", BranchName: "staging", Rollout: rollout},
		RequestedBranch: requested,
		Surfing:         surfing,
	}
}

func TestBranchSurfRuleServesTheRequestedBranch(t *testing.T) {
	candidates, matched, err := (&branchSurfRule{}).Evaluate(
		context.Background(),
		surfRequest("pr-482", types.BranchSurfing{Enabled: true, Pattern: "pr-*"}, nil),
	)

	require.NoError(t, err)
	require.True(t, matched)
	// The mapped branch trails as the runtime-version fallback.
	assert.Equal(t, []string{"pr-482", "staging"}, candidates)
}

func TestBranchSurfRuleDeclines(t *testing.T) {
	on := types.BranchSurfing{Enabled: true, Pattern: "pr-*"}
	cases := map[string]*BranchResolutionRequest{
		"no branch requested":  surfRequest("", on, nil),
		"surfing off":          surfRequest("pr-482", types.BranchSurfing{Enabled: false, Pattern: "*"}, nil),
		"pattern mismatch":     surfRequest("hotfix-1", on, nil),
		"already mapped":       surfRequest("staging", types.BranchSurfing{Enabled: true, Pattern: "*"}, nil),
		"empty pattern denies": surfRequest("pr-482", types.BranchSurfing{Enabled: true, Pattern: ""}, nil),
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, matched, err := (&branchSurfRule{}).Evaluate(context.Background(), req)
			require.NoError(t, err)
			assert.False(t, matched)
		})
	}
}

// Asking for the branch the channel already maps to is not a surf, so the chain
// carries on to the rollout rule instead of pinning the device out of its bucket.
func TestBranchSurfRuleKeepsTheRolloutWhenAskedForTheMappedBranch(t *testing.T) {
	rollout := &expo.ChannelRolloutInfo{ID: "r1", BranchName: "canary", Percentage: 100}
	req := surfRequest("staging", types.BranchSurfing{Enabled: true, Pattern: "*"}, rollout)

	candidates, err := ResolveBranchCandidates(context.Background(), DefaultBranchRules(), req)

	require.NoError(t, err)
	assert.Equal(t, []string{"canary", "staging"}, candidates)
}

// A surf wins over an active rollout: it is an explicit choice, not a draw.
func TestBranchSurfRuleOutranksTheRollout(t *testing.T) {
	rollout := &expo.ChannelRolloutInfo{ID: "r1", BranchName: "canary", Percentage: 100}
	req := surfRequest("pr-482", types.BranchSurfing{Enabled: true, Pattern: "pr-*"}, rollout)

	candidates, err := ResolveBranchCandidates(context.Background(), DefaultBranchRules(), req)

	require.NoError(t, err)
	assert.Equal(t, []string{"pr-482", "staging"}, candidates)
}

func TestBranchSurfRuleIsInertWithoutARequestedBranch(t *testing.T) {
	rollout := &expo.ChannelRolloutInfo{ID: "r1", BranchName: "canary", Percentage: 100}
	req := surfRequest("", types.BranchSurfing{}, rollout)

	candidates, err := ResolveBranchCandidates(context.Background(), DefaultBranchRules(), req)

	require.NoError(t, err)
	assert.Equal(t, []string{"canary", "staging"}, candidates)
}
