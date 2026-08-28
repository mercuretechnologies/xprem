package services

import (
	"context"
	"xprem/internal/branch"
	"xprem/internal/rollout"
	"xprem/internal/types"
)

// BranchResolutionRequest carries everything a rule needs to decide which branches a
// device may be served from. Mapping is the channel's resolved mapping, including the
// active channel rollout when one exists (control-plane mode only).
type BranchResolutionRequest struct {
	AppID          string
	ChannelName    string
	ClientID       string
	Platform       types.Platform
	RuntimeVersion string
	Mapping        *types.ChannelResolution
	// RequestedBranch is the xprem-branch header, empty when the device asks for
	// nothing in particular. Surfing is the channel's setting for it.
	RequestedBranch string
	Surfing         types.BranchSurfing
}

// BranchRule is one step of the ordered channel-resolution chain evaluated at
// manifest/asset time. The first rule that matches wins and returns the branch
// candidates in priority order; the candidate order IS the runtime-version fallback
// (resolution serves the first candidate that has an update for the device's runtime
// version and platform). Future enterprise targeting rules are prepended to the slice
// in wire.go, ahead of the default rules.
type BranchRule interface {
	Evaluate(ctx context.Context, req *BranchResolutionRequest) (candidates []string, matched bool, err error)
}

// DefaultBranchRules is the MIT rule chain: the branch the device asked for when the
// channel allows it, then the channel-rollout percentage split when one is active,
// then the plain mapped branch.
func DefaultBranchRules() []BranchRule {
	return []BranchRule{&branchSurfRule{}, &channelRolloutPercentRule{}, &defaultBranchRule{}}
}

// ResolveBranchCandidates evaluates the rules in order and returns the first match's
// candidates. The mapped branch is the safety net should no rule match (the default
// rule always does).
func ResolveBranchCandidates(ctx context.Context, rules []BranchRule, req *BranchResolutionRequest) ([]string, error) {
	for _, rule := range rules {
		candidates, matched, err := rule.Evaluate(ctx, req)
		if err != nil {
			return nil, err
		}
		if matched {
			return candidates, nil
		}
	}
	return []string{req.Mapping.BranchName}, nil
}

// branchSurfRule serves the branch the device asked for. Ahead of the rollout rule: a
// surf is an explicit choice and does not go back through the bucket draw. Asking for
// the mapped branch is not a surf, so it falls through and keeps the rollout.
type branchSurfRule struct{}

// HonoursSurf reports whether the chain will serve the branch the device asked for.
// Anything downstream that needs to know "is this poll actually surfing" must ask
// here rather than re-test the header, or it treats declined requests as surfs.
func HonoursSurf(req *BranchResolutionRequest) bool {
	return req.RequestedBranch != "" &&
		req.Surfing.Enabled &&
		req.RequestedBranch != req.Mapping.BranchName &&
		branch.MatchPattern(req.Surfing.Pattern, req.RequestedBranch)
}

func (r *branchSurfRule) Evaluate(ctx context.Context, req *BranchResolutionRequest) ([]string, bool, error) {
	if !HonoursSurf(req) {
		return nil, false, nil
	}
	// The mapped branch stays a candidate: a surfed branch with no update for the
	// device's runtime version falls back instead of stranding it.
	return []string{req.RequestedBranch, req.Mapping.BranchName}, true, nil
}

// channelRolloutPercentRule implements the channel rollout split: devices bucketed
// under the rollout percentage get the rollout branch, everyone else the mapped
// branch. The rollout row's UUID is the bucketing salt, fixed for the rollout's life.
// In-bucket devices keep the mapped branch as a second candidate so a rollout branch
// with no update for the device's runtime version falls back to the default branch.
type channelRolloutPercentRule struct{}

func (r *channelRolloutPercentRule) Evaluate(ctx context.Context, req *BranchResolutionRequest) ([]string, bool, error) {
	channelRollout := req.Mapping.Rollout
	if channelRollout == nil {
		return nil, false, nil
	}
	if rollout.InBucket(req.ClientID, channelRollout.ID, channelRollout.Percentage) {
		return []string{channelRollout.BranchName, req.Mapping.BranchName}, true, nil
	}
	return []string{req.Mapping.BranchName}, true, nil
}

// defaultBranchRule terminates the chain: every device gets the channel's mapped
// branch.
type defaultBranchRule struct{}

func (r *defaultBranchRule) Evaluate(ctx context.Context, req *BranchResolutionRequest) ([]string, bool, error) {
	return []string{req.Mapping.BranchName}, true, nil
}
