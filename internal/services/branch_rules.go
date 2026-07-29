package services

import (
	"context"
	"xprem/internal/providers/expo"
	"xprem/internal/rollout"
)

// BranchResolutionRequest carries everything a rule needs to decide which branches a
// device may be served from. Mapping is the channel's resolved mapping, including the
// active channel rollout when one exists (control-plane mode only).
type BranchResolutionRequest struct {
	AppID          string
	ChannelName    string
	ClientID       string
	Platform       string
	RuntimeVersion string
	Mapping        *expo.ChannelMapping
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

// DefaultBranchRules is the MIT rule chain: the channel-rollout percentage split when
// one is active, then the plain mapped branch.
func DefaultBranchRules() []BranchRule {
	return []BranchRule{&channelRolloutPercentRule{}, &defaultBranchRule{}}
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
