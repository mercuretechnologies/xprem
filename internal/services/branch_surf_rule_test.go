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

// Deliberate, and pinned here so it reads as a decision rather than an oversight.
//
// A channel mid-rollout has two branches: the one it maps to, and the rollout
// target. Asking for the MAPPED branch is not a surf — it falls through and the
// device stays subject to the percentage draw. Asking for the ROLLOUT TARGET is
// a surf, and it is honoured: the device pins onto that branch without being
// drawn. That is the point of the feature — the rollout target is a branch under
// test, and a tester must be able to reach it on demand.
//
// The cost, accepted: such a device counts in that update's health without
// having been selected into the rollout. Reverse this and the rule below must
// exclude Rollout.BranchName, and ListSurfableBranches must stop listing it.
func TestSurfingOntoTheRolloutTargetIsHonoured(t *testing.T) {
	rollout := &expo.ChannelRolloutInfo{BranchName: "canary", Percentage: 5}
	on := types.BranchSurfing{Enabled: true, Pattern: "*"}

	assert.True(t, HonoursSurf(surfRequest("canary", on, rollout)),
		"the rollout target is reachable on demand")
	assert.False(t, HonoursSurf(surfRequest("staging", on, rollout)),
		"the mapped branch is not a surf, so the percentage draw still decides")
}
