package services

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"xprem/internal/auditlog"
	"xprem/internal/branch"
	"xprem/internal/cache"
	"xprem/internal/dashboard"
	"xprem/internal/types"
	"xprem/internal/validation"
)

// invalidateChannelCaches drops the dashboard list caches a channel write
// stales, so every write surface (dashboard, MCP) stays coherent.
func invalidateChannelCaches(appId string) {
	appCache := cache.GetCache()
	appCache.Delete(dashboard.ComputeGetChannelsCacheKey(appId))
	appCache.Delete(dashboard.ComputeGetBranchesCacheKey(appId))
}

type ChannelService struct {
	branchRepo  BranchRepository
	channelRepo ChannelRepository
	// onAuditEvent is the audit emission seam; nil (community) means channel
	// changes leave no events.
	onAuditEvent auditlog.RecordFunc
}

type ChannelRepository interface {
	InsertChannel(ctx context.Context, appId string, branchId *int64, channelName string) (int64, error)
	DeleteChannel(ctx context.Context, channelName string, appId string) error
	GetChannelNameByBranchName(ctx context.Context, appId string, branchName string) ([]string, error)
	GetChannels(ctx context.Context, appId string) ([]types.ChannelMapping, error)
	GetChannelBranchMapping(ctx context.Context, appId string, channelName string) (*types.ChannelResolution, error)
	// GetBranchSurfing returns nil, nil when the channel does not exist.
	GetBranchSurfing(ctx context.Context, appId string, channelName string) (*types.BranchSurfing, error)
	SetBranchSurfing(ctx context.Context, appId string, channelName string, surfing types.BranchSurfing) error
}

// SetOnAuditEvent plugs the audit emission seam (see SetSSOEnforced for the
// pattern). Nil-safe.
func (s *ChannelService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

func NewChannelService(branchRepo BranchRepository, channelRepo ChannelRepository) *ChannelService {
	return &ChannelService{
		branchRepo:  branchRepo,
		channelRepo: channelRepo,
	}
}

func (s *ChannelService) CreateChannel(ctx context.Context, appId string, branchName *string, channelName string) (int64, error) {
	if err := validation.Name("channelName", channelName); err != nil {
		return 0, err
	}
	var branchIdPtr *int64
	if branchName != nil {
		if err := validation.Name("branchName", *branchName); err != nil {
			return 0, err
		}
		branchId, err := s.branchRepo.GetBranchByName(ctx, appId, *branchName)
		if err != nil {
			return 0, fmt.Errorf("failed to map channel: target branch '%s' does not exist: %w", *branchName, err)
		}
		branchIdPtr = &branchId
	}
	channelId, err := s.channelRepo.InsertChannel(ctx, appId, branchIdPtr, channelName)
	if err != nil {
		return 0, err
	}
	// Channels are addressed by name everywhere (routes, expo-channel-name):
	// the name is the target id, the numeric id an annotation. Ids travel as
	// strings in metadata: an int64 as a JSON number corrupts past 2^53 in
	// the dashboard's JavaScript.
	metadata := map[string]any{"channel_id": strconv.FormatInt(channelId, 10)}
	if branchName != nil {
		metadata["branch"] = *branchName
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionChannelCreated,
		TargetType:    "channel",
		TargetID:      channelName,
		TargetDisplay: channelName,
		AppID:         appId,
		Metadata:      metadata,
	})
	invalidateChannelCaches(appId)
	// A channel that did not exist is now cached as "no surfing" for the TTL, so
	// creating one under a name that was ever asked for would answer 404 until
	// that entry aged out.
	invalidateBranchSurfingCache(appId, channelName)
	return channelId, nil
}

func (s *ChannelService) DeleteChannel(ctx context.Context, channelName string, appId string) error {
	if err := validation.Name("channelName", channelName); err != nil {
		return err
	}
	err := s.channelRepo.DeleteChannel(ctx, channelName, appId)
	if err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionChannelDeleted,
		TargetType:    "channel",
		TargetID:      channelName,
		TargetDisplay: channelName,
		AppID:         appId,
	})
	invalidateChannelCaches(appId)
	// The surfing setting is cached per channel name, not per channel row, so a
	// deleted channel would keep answering /branch_lists for the rest of the TTL
	// — and a channel recreated under the same name would inherit the permission
	// it was never given.
	invalidateBranchSurfingCache(appId, channelName)
	return nil
}

func (s *ChannelService) GetChannels(ctx context.Context, appId string) ([]types.ChannelMapping, error) {
	return s.channelRepo.GetChannels(ctx, appId)
}

func (s *ChannelService) GetBranchSurfing(ctx context.Context, appId string, channelName string) (*types.BranchSurfing, error) {
	if err := validation.Name("channelName", channelName); err != nil {
		return nil, err
	}
	return s.channelRepo.GetBranchSurfing(ctx, appId, channelName)
}

func (s *ChannelService) SetBranchSurfing(ctx context.Context, appId string, channelName string, surfing types.BranchSurfing) error {
	if err := validation.Name("channelName", channelName); err != nil {
		return err
	}
	if err := validation.NamePattern("branchSurfingPattern", surfing.Pattern); err != nil {
		return err
	}
	// Collapsed on write, as API key access rules are, so patterns naming the
	// same set of branches are stored identically.
	surfing.Pattern = branch.CollapseWildcards(surfing.Pattern)
	if err := s.channelRepo.SetBranchSurfing(ctx, appId, channelName, surfing); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionBranchSurfingUpdated,
		TargetType:    "channel",
		TargetID:      channelName,
		TargetDisplay: channelName,
		AppID:         appId,
		Metadata: map[string]any{
			"enabled": surfing.Enabled,
			"pattern": surfing.Pattern,
		},
	})
	invalidateChannelCaches(appId)
	invalidateBranchSurfingCache(appId, channelName)
	return nil
}

// The page every device gets at launch, and the ceiling when one asks for the
// whole list. In acceptance testing the branch being looked for was published
// recently, so the first page is the product behaviour and not merely a bound.
// Ten rather than something roomier so that a team with a few dozen open
// branches actually meets the "see all" path instead of it lying dormant until
// some rare scale. The second bound exists so "see all" cannot be turned into an
// unbounded download.
const (
	maxSurfableBranches    = 10
	maxAllSurfableBranches = 500
)

// ListSurfableBranches returns the branches a device polling channelName may ask
// to be served: those matching the channel's pattern that have a published
// update for the device's runtime version AND platform, newest first. Only the first page is
// returned unless all is set, but Total always counts every match, so a client
// can tell it is looking at part of the list. It refuses a channel that does not
// exist or has branch surfing off.
func (s *ChannelService) ListSurfableBranches(ctx context.Context, appId string, channelName string, runtimeVersion string, platform string, all bool) (types.SurfableBranchList, error) {
	if err := validation.Name("channelName", channelName); err != nil {
		return types.SurfableBranchList{}, err
	}
	enabled, pattern := cachedBranchSurfing(ctx, s.channelRepo, appId, channelName)
	// An unknown channel and a channel with surfing off answer the same way, so
	// the endpoint never reveals which channels exist.
	if !enabled {
		return types.SurfableBranchList{}, &ExpoProtocolError{StatusCode: http.StatusNotFound, Message: "Branch surfing is not enabled for this channel"}
	}
	branches, err := cachedSurfableBranches(ctx, s.branchRepo, appId, runtimeVersion, platform)
	if err != nil {
		return types.SurfableBranchList{}, err
	}
	// The channel's own branch is left out because asking for it is deliberately
	// treated as asking for nothing, so picking it here would do nothing and look
	// broken. Going back is the client's reset, which drops the override.
	//
	// Only that branch. A channel mid-rollout also has Mapping.Rollout.BranchName,
	// and that one IS offered and IS honoured: a tester asking for the rollout
	// target pins onto it, skipping the percentage draw. That is the point of the
	// feature — it is the branch under test. The cost is that such a device counts
	// in the rollout's health without having been drawn into it.
	// Failing rather than degrading: swallowing the error leaves mappedBranch
	// empty, which turns the filter below into a no-op and silently puts the
	// unselectable branch back in the picker. A panel that reports an error is
	// recoverable; a list that is quietly wrong is not noticed.
	mapping, err := cachedChannelMapping(ctx, s.channelRepo, appId, channelName)
	if err != nil {
		return types.SurfableBranchList{}, err
	}
	var mappedBranch string
	if mapping != nil {
		mappedBranch = mapping.BranchName
	}
	limit := maxSurfableBranches
	if all {
		limit = maxAllSurfableBranches
	}
	// The cap lands after the pattern filter: a narrow pattern whose matches are
	// all older than the cut must still find them.
	matched := make([]types.SurfableBranch, 0, limit)
	total := 0
	for _, candidate := range branches {
		if candidate.Name == mappedBranch || !branch.MatchPattern(pattern, candidate.Name) {
			continue
		}
		total++
		if len(matched) < limit {
			matched = append(matched, candidate)
		}
	}
	return types.SurfableBranchList{Branches: matched, Total: total}, nil
}
