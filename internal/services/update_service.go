package services

import (
	"context"
	"fmt"
	"xprem/internal/bucket"
	"xprem/internal/cache"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/rollout"
	"xprem/internal/types"
	update2 "xprem/internal/update"
	"xprem/internal/validation"
)

type UpdateRepository interface {
	MarkUpdateAsChecked(ctx context.Context, update types.Update) error
	GetUpdateDetails(ctx context.Context, appId string, branchName string, runtimeVersion string, updateId string) (types.UpdateDetails, error)
	GetUpdate(ctx context.Context, appId string, branchName string, runtimeVersion string, updateId string) (*types.Update, error)
	GetLatestUpdate(ctx context.Context, appId string, branchName string, runtimeVersion string, platform types.Platform) (*types.Update, error)
	GetLatestUpdateWithRollout(ctx context.Context, appId string, branchName string, runtimeVersion string, platform types.Platform) (*types.UpdateWithRollout, error)
	GetUpdateByUUID(ctx context.Context, appId string, updateUUID string) (*types.Update, error)
	HasActiveRolloutUpdate(ctx context.Context, appId string, branchName string, runtimeVersion string) (bool, error)
	GetUpdateType(ctx context.Context, update types.Update) (types.UpdateType, error)
	IsUpdateValid(ctx context.Context, update types.Update) (bool, error)
	// publishGroup, when non-nil, is the UUID shared by every per-platform
	// update row of one eoas run (CLI-minted on publish, server-minted on
	// group republish) so consumers can treat them as a single publish. Nil
	// (older CLIs, rollbacks, internal callers) leaves the rows ungrouped;
	// the bucket store ignores it entirely (no grouping in stateless mode).
	CreateUpdate(ctx context.Context, appId string, updateId int64, branchName string, runtimeVersion string, platform types.Platform, commitHash string, message string, publishGroup *string) (*types.Update, error)
	CreateUpdateWithRollout(ctx context.Context, appId string, updateId int64, branchName string, runtimeVersion string, platform types.Platform, commitHash string, message string, rolloutPercentage int, publishGroup *string) (*types.Update, error)
	// message is the reason the rollback was created. Empty for the CLI and
	// for the rollout revert, which have none to give; the dashboard requires
	// one so the row says why the fleet was sent back to the embedded bundle.
	CreateRollback(ctx context.Context, appId string, updateId int64, branchName string, runtimeVersion string, platform types.Platform, commitHash string, message string) (*types.Update, error)
	// GetUpdatesByPublishGroup resolves the checked members of one publish
	// group on (branch, runtime version), for the group republish.
	// Control-plane only: the bucket store answers ErrNotSupportedInStatelessMode.
	GetUpdatesByPublishGroup(ctx context.Context, appId string, branchName string, runtimeVersion string, publishGroup string) ([]types.PublishGroupMember, error)
	GetPublishGroupsPage(ctx context.Context, appId string, branchName string, runtimeVersion string, cursor *int64, limit int) (types.PublishGroupsPage, error)
	GetUpdateByBranchNameAndRuntime(ctx context.Context, appId string, updateId int64, branchName string, runtimeVersion string) (pgdb.GetUpdateByBranchNameAndRuntimeRow, error)
	GetUpdatesByRunTimeVersionAndBranchName(ctx context.Context, appId string, runtimeVersion string, branchName string, cursor *int64, limit int) (types.UpdatesPage, error)
	GetUpdateFeed(ctx context.Context, appId string, query types.UpdateFeedQuery) ([]types.UpdateFeedItem, error)
	RetrieveUpdateStoredMetadata(ctx context.Context, update types.Update) (*types.UpdateStoredMetadata, error)
	StoreUpdateUUIDInMetadata(ctx context.Context, update types.Update, updateUUID string) error
}

type UpdateService struct {
	updateRepo UpdateRepository
	bucket     bucket.Bucket
}

func NewUpdateService(updateRepo UpdateRepository, bucket bucket.Bucket) *UpdateService {
	return &UpdateService{
		updateRepo: updateRepo,
		bucket:     bucket,
	}
}

// getLatestUpdateEnvelope is the cached read underneath GetLatestUpdate and
// GetLatestUpdateForClient: the flat UpdateWithRollout envelope (update + per-update
// rollout state + embedded control) stored under the lastUpdate cache key. A nil
// envelope (no checked update yet) is deliberately never cached.
func (s *UpdateService) getLatestUpdateEnvelope(ctx context.Context, appId string, branchName string, runtimeVersion string, platform types.Platform) (*types.UpdateWithRollout, error) {
	envelopeCache := cache.GetCache()
	cacheKey := update2.ComputeLastUpdateCacheKey(appId, branchName, runtimeVersion, platform)
	if cachedEnvelope, ok := cache.GetJSON[types.UpdateWithRollout](envelopeCache, cacheKey); ok {
		return &cachedEnvelope, nil
	}
	latestEnvelope, err := s.updateRepo.GetLatestUpdateWithRollout(ctx, appId, branchName, runtimeVersion, platform)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve latest update from store: %w", err)
	}
	if latestEnvelope == nil {
		return nil, nil
	}
	ttl := 1800
	cache.SetJSON(envelopeCache, cacheKey, latestEnvelope, &ttl)
	return latestEnvelope, nil
}

func (s *UpdateService) GetLatestUpdate(ctx context.Context, appId string, branchName string, runtimeVersion string, platform types.Platform) (*types.Update, error) {
	envelope, err := s.getLatestUpdateEnvelope(ctx, appId, branchName, runtimeVersion, platform)
	if err != nil || envelope == nil {
		return nil, err
	}
	return &envelope.Update, nil
}

// ClientUpdateResolution is the per-device answer of GetLatestUpdateForClient.
// BranchHasUpdate distinguishes "this branch has nothing for the runtime version"
// (callers may fall back to another branch) from "this branch resolved for the device"
// with a possibly nil Update (out-of-bucket with no control => noUpdateAvailable, no
// fallback).
type ClientUpdateResolution struct {
	Update          *types.Update
	BranchHasUpdate bool
}

// GetLatestUpdateForClient resolves the update a specific device should receive from a
// branch, applying the per-update rollout decision tree: no rollout or in-bucket =>
// latest update; out-of-bucket => the control update (nil control => noUpdateAvailable).
// The control substitution happens here, before any response composition, so the
// same-current-id short-circuit keeps working for devices already on the control.
func (s *UpdateService) GetLatestUpdateForClient(ctx context.Context, appId string, branchName string, runtimeVersion string, platform types.Platform, clientID string) (ClientUpdateResolution, error) {
	envelope, err := s.getLatestUpdateEnvelope(ctx, appId, branchName, runtimeVersion, platform)
	if err != nil {
		return ClientUpdateResolution{}, err
	}
	if envelope == nil {
		return ClientUpdateResolution{}, nil
	}
	if envelope.RolloutPercentage == nil {
		return ClientUpdateResolution{Update: &envelope.Update, BranchHasUpdate: true}, nil
	}
	salt := rollout.UpdateSalt(appId, branchName, runtimeVersion, envelope.UpdateId)
	if rollout.InBucket(clientID, salt, *envelope.RolloutPercentage) {
		return ClientUpdateResolution{Update: &envelope.Update, BranchHasUpdate: true}, nil
	}
	return ClientUpdateResolution{Update: envelope.Control, BranchHasUpdate: true}, nil
}

func (s *UpdateService) GetUpdateDetails(ctx context.Context, appId string, branchName string, runtimeVersion string, updateId string) (types.UpdateDetails, error) {
	if err := validation.Name("branchName", branchName); err != nil {
		return types.UpdateDetails{}, err
	}
	if err := validation.Name("runtimeVersion", runtimeVersion); err != nil {
		return types.UpdateDetails{}, err
	}
	if err := validation.Name("updateId", updateId); err != nil {
		return types.UpdateDetails{}, err
	}
	return s.updateRepo.GetUpdateDetails(ctx, appId, branchName, runtimeVersion, updateId)
}

func (s *UpdateService) GetUpdatesByRunTimeVersionAndBranchName(ctx context.Context, appId string, runtimeVersion string, branchName string, cursor *int64, limit int) (types.UpdatesPage, error) {
	if err := validation.Name("branchName", branchName); err != nil {
		return types.UpdatesPage{}, err
	}
	if err := validation.Name("runtimeVersion", runtimeVersion); err != nil {
		return types.UpdatesPage{}, err
	}
	return s.updateRepo.GetUpdatesByRunTimeVersionAndBranchName(ctx, appId, runtimeVersion, branchName, cursor, limit)
}

func (s *UpdateService) GetPublishGroupsPage(ctx context.Context, appId string, runtimeVersion string, branchName string, cursor *int64, limit int) (types.PublishGroupsPage, error) {
	if err := validation.Name("branchName", branchName); err != nil {
		return types.PublishGroupsPage{}, err
	}
	if err := validation.Name("runtimeVersion", runtimeVersion); err != nil {
		return types.PublishGroupsPage{}, err
	}
	return s.updateRepo.GetPublishGroupsPage(ctx, appId, branchName, runtimeVersion, cursor, limit)
}

func (s *UpdateService) GetUpdateFeed(ctx context.Context, appId string, query types.UpdateFeedQuery) ([]types.UpdateFeedItem, error) {
	return s.updateRepo.GetUpdateFeed(ctx, appId, query)
}
