package services

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"xprem/internal/auditlog"
	"xprem/internal/bucket"
	"xprem/internal/cache"
	"xprem/internal/dashboard"
	"xprem/internal/store"
	"xprem/internal/types"
	update2 "xprem/internal/update"
	"xprem/internal/validation"
)

type BranchService struct {
	branchRepo  BranchRepository
	channelRepo ChannelRepository
	updateRepo  UpdateRepository
	// Nil in stateless mode, where rollouts do not exist and the guards below are inert.
	rolloutRepo RolloutRepository
	bucket      bucket.Bucket
	// onAuditEvent is the audit emission seam; nil (community) means branch
	// changes leave no events.
	onAuditEvent auditlog.RecordFunc
}

type BranchRepository interface {
	InsertBranch(ctx context.Context, appId string, branchName string) (int64, error)
	UpsertBranchAndRuntimeVersion(ctx context.Context, appId string, branchName string, runtimeVersion string) error
	GetUpdateRefsByBranchName(ctx context.Context, appId string, branchName string) ([]types.UpdateRef, error)
	DeleteBranchByName(ctx context.Context, appId string, branchName string) error
	GetBranches(ctx context.Context, appId string) ([]types.BranchMapping, error)
	GetSurfableBranches(ctx context.Context, appId string, runtimeVersion string, platform types.Platform) ([]types.SurfableBranch, error)
	GetRuntimeVersionsWithUpdateStats(ctx context.Context, appId string, branchName string) ([]types.RuntimeVersionWithStats, error)
	UpdateChannelBranchMapping(ctx context.Context, appId string, channelId string, branchId string) error
	CreateRuntimeVersion(ctx context.Context, appId string, version string) (int64, error)
	GetBranchByName(ctx context.Context, appId string, branchName string) (int64, error)
}

// SetOnAuditEvent plugs the audit emission seam (see SetSSOEnforced for the
// pattern). Nil-safe.
func (s *BranchService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

func NewBranchService(branchRepo BranchRepository, channelRepo ChannelRepository, updateRepo UpdateRepository, rolloutRepo RolloutRepository, bucket bucket.Bucket) *BranchService {
	return &BranchService{
		branchRepo:  branchRepo,
		channelRepo: channelRepo,
		updateRepo:  updateRepo,
		rolloutRepo: rolloutRepo,
		bucket:      bucket,
	}
}

func (s *BranchService) CreateBranch(ctx context.Context, appId string, branchName string) (int64, error) {
	if err := validation.Name("branchName", branchName); err != nil {
		return 0, err
	}
	if bucket.ReservedBranchName(branchName) {
		return 0, validation.Errorf("branchName", "%q is reserved", branchName)
	}
	branchId, err := s.branchRepo.InsertBranch(ctx, appId, branchName)
	if err != nil {
		return 0, err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionBranchCreated,
		TargetType:    "branch",
		TargetID:      branchName,
		TargetDisplay: branchName,
		AppID:         appId,
		// Ids travel as strings in metadata: an int64 as a JSON number
		// corrupts past 2^53 in the dashboard's JavaScript.
		Metadata: map[string]any{"branch_id": strconv.FormatInt(branchId, 10)},
	})
	cache.GetCache().Delete(dashboard.ComputeGetBranchesCacheKey(appId))
	return branchId, nil
}

func (s *BranchService) DeleteBranch(ctx context.Context, branchName string, appId string) error {
	if err := validation.Name("branchName", branchName); err != nil {
		return err
	}
	channels, err := s.channelRepo.GetChannelNameByBranchName(ctx, appId, branchName)
	if err != nil {
		return fmt.Errorf("failed to validate branch dependencies: %w", err)
	}
	if len(channels) > 0 {
		return &store.ErrBranchHasActiveChannels{
			BranchName:   branchName,
			ChannelNames: channels,
		}
	}
	// Friendly sibling of the FK RESTRICT on channel_rollouts.rollout_branch_id: a
	// branch serving an active rollout cannot be deleted, and the error names the
	// channels to unblock instead of surfacing a raw constraint violation.
	if s.rolloutRepo != nil {
		rolloutChannels, err := s.rolloutRepo.GetChannelRolloutsByBranch(ctx, appId, branchName)
		if err != nil {
			return fmt.Errorf("failed to validate branch rollout dependencies: %w", err)
		}
		if len(rolloutChannels) > 0 {
			return &store.ErrBranchInActiveRollout{
				BranchName:   branchName,
				ChannelNames: rolloutChannels,
			}
		}
	}
	rows, err := s.branchRepo.GetUpdateRefsByBranchName(ctx, appId, branchName)
	if err != nil {
		return fmt.Errorf("failed to retrieve updates linked to the branch from database: %w", err)
	}
	err = s.branchRepo.DeleteBranchByName(ctx, appId, branchName)
	if err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionBranchDeleted,
		TargetType:    "branch",
		TargetID:      branchName,
		TargetDisplay: branchName,
		AppID:         appId,
	})
	appCache := cache.GetCache()
	appCache.Delete(dashboard.ComputeGetBranchesCacheKey(appId))
	appCache.Delete(dashboard.ComputeGetRuntimeVersionsCacheKey(appId, branchName))
	// Without this a surfing device could still be handed the deleted branch's
	// cached envelope while its files are being removed below.
	purgedRuntimeVersions := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if _, purged := purgedRuntimeVersions[row.RuntimeVersion]; purged {
			continue
		}
		purgedRuntimeVersions[row.RuntimeVersion] = struct{}{}
		for _, platform := range []types.Platform{types.PlatformIOS, types.PlatformAndroid} {
			appCache.Delete(update2.ComputeLastUpdateCacheKey(appId, branchName, row.RuntimeVersion, platform))
		}
	}
	go func(bucketRows []types.UpdateRef) {
		for _, row := range bucketRows {
			err := s.bucket.DeleteUpdateFolder(appId, branchName, row.RuntimeVersion, strconv.FormatInt(row.ID, 10))
			if err != nil {
				fmt.Printf("failed to delete update files for update %d: %v\n", row.ID, err)
			}
		}
	}(rows)
	return nil
}

func (s *BranchService) GetBranches(ctx context.Context, appId string) ([]types.BranchMapping, error) {
	return s.branchRepo.GetBranches(ctx, appId)
}

func (s *BranchService) GetRuntimeVersionsWithUpdateStats(ctx context.Context, appId string, branchName string) ([]types.RuntimeVersionWithStats, error) {
	if err := validation.Name("branchName", branchName); err != nil {
		return nil, err
	}
	return s.branchRepo.GetRuntimeVersionsWithUpdateStats(ctx, appId, branchName)
}

func (s *BranchService) UpdateChannelBranchMapping(ctx context.Context, appId string, channelId string, channelName string, branchId string) error {
	// channelId is the release channel's id, not its name. Both ids are
	// backend-dependent (numeric on the DB control plane, provider id strings on
	// the bucket backend), so validate them as safe segments rather than forcing
	// numeric. channelName only exists to disambiguate the rollout-locked case,
	// whose repository is keyed by name.
	if err := validation.Name("releaseChannelId", channelId); err != nil {
		return err
	}
	if err := validation.Name("branchId", branchId); err != nil {
		return err
	}
	err := s.branchRepo.UpdateChannelBranchMapping(ctx, appId, channelId, branchId)
	if err != nil {
		// The guarded UPDATE reports 0 rows for both an unknown channel and a channel
		// locked by an active rollout; tell them apart so the caller gets a 409 with
		// the real reason instead of a misleading 404.
		var notFoundErr *store.ErrResourceNotFound
		if errors.As(err, &notFoundErr) && notFoundErr.Resource == "channel" && s.rolloutRepo != nil && channelName != "" {
			activeRollout, rolloutErr := s.rolloutRepo.GetChannelRollout(ctx, appId, channelName)
			if rolloutErr == nil && activeRollout != nil {
				return &store.ErrChannelHasActiveRollout{ChannelName: channelName}
			}
		}
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionChannelBranchMapped,
		TargetType:    "channel",
		TargetID:      channelName,
		TargetDisplay: channelName,
		AppID:         appId,
		Metadata:      map[string]any{"channel_id": channelId, "branch_id": branchId},
	})
	return nil
}

func (s *BranchService) UpsertBranchAndRuntimeVersion(ctx context.Context, appId string, branchName string, runtimeVersion string) error {
	if err := validation.Name("branchName", branchName); err != nil {
		return err
	}
	if bucket.ReservedBranchName(branchName) {
		return validation.Errorf("branchName", "%q is reserved", branchName)
	}
	return s.branchRepo.UpsertBranchAndRuntimeVersion(ctx, appId, branchName, runtimeVersion)
}
