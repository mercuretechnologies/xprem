package services

import (
	"context"
	"expo-open-ota/internal/auditlog"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/dashboard"
	"expo-open-ota/internal/providers/expo"
	"expo-open-ota/internal/types"
	"expo-open-ota/internal/validation"
	"fmt"
	"strconv"
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
	GetChannelBranchMapping(ctx context.Context, appId string, channelName string) (*expo.ChannelMapping, error)
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
	return nil
}

func (s *ChannelService) GetChannels(ctx context.Context, appId string) ([]types.ChannelMapping, error) {
	return s.channelRepo.GetChannels(ctx, appId)
}
