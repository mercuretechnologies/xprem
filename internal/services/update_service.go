package services

import (
	"context"
	"encoding/json"
	"fmt"
	"xprem/internal/bucket"
	"xprem/internal/cache"
	"xprem/internal/rollout"
	"xprem/internal/store"
	"xprem/internal/types"
	update2 "xprem/internal/update"
	"xprem/internal/validation"

	"golang.org/x/sync/singleflight"
)

type UpdateRepository interface {
	MarkUpdateAsChecked(ctx context.Context, update types.Update) error
	GetUpdateDetails(ctx context.Context, appId string, branchName string, runtimeVersion string, updateId string) (types.UpdateDetails, error)
	GetUpdate(ctx context.Context, appId string, branchName string, runtimeVersion string, updateId string) (*types.Update, error)
	// GetCheckedUpdate is GetUpdate restricted to complete updates: nil unless
	// the row exists and has been checked.
	GetCheckedUpdate(ctx context.Context, appId string, branchName string, runtimeVersion string, updateId string) (*types.Update, error)
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
	// ImportUpdate copies one externally-published update row; false means the
	// row already existed.
	// Control-plane only: the bucket store answers ErrNotSupportedInStatelessMode.
	ImportUpdate(ctx context.Context, params store.ImportUpdateParams) (bool, error)
	// Control-plane only: the bucket store answers ErrNotSupportedInStatelessMode.
	UpdateExists(ctx context.Context, appId string, branchName string, updateId int64) (bool, error)
	// GetUpdatesByPublishGroup resolves the checked members of one publish
	// group on (branch, runtime version), for the group republish.
	// Control-plane only: the bucket store answers ErrNotSupportedInStatelessMode.
	GetUpdatesByPublishGroup(ctx context.Context, appId string, branchName string, runtimeVersion string, publishGroup string) ([]types.PublishGroupMember, error)
	GetPublishGroupsPage(ctx context.Context, appId string, branchName string, runtimeVersion string, cursor *int64, limit int) (types.PublishGroupsPage, error)
	GetUpdatesByRunTimeVersionAndBranchName(ctx context.Context, appId string, runtimeVersion string, branchName string, cursor *int64, limit int) (types.UpdatesPage, error)
	GetUpdateFeed(ctx context.Context, appId string, query types.UpdateFeedQuery) ([]types.UpdateFeedItem, error)
	RetrieveUpdateStoredMetadata(ctx context.Context, update types.Update) (*types.UpdateStoredMetadata, error)
	StoreUpdateUUIDInMetadata(ctx context.Context, update types.Update, updateUUID string) error
	// GetUpdateAssetMapping answers nil for an update published before the
	// mapping existed; callers fall back to the update-folder layout.
	GetUpdateAssetMapping(ctx context.Context, update types.Update) (*types.UpdateAssetMapping, error)
	StoreUpdateAssetMapping(ctx context.Context, update types.Update, mapping *types.UpdateAssetMapping) error
}

type UpdateService struct {
	updateRepo UpdateRepository
	bucket     bucket.Bucket
	// manifestFlight collapses concurrent composes of one manifest entry:
	// right after a publish, every poll misses the cache at once.
	manifestFlight singleflight.Group
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
	ttl := lastUpdateEnvelopeCacheTTLSeconds
	cache.SetJSON(envelopeCache, cacheKey, latestEnvelope, &ttl)
	return latestEnvelope, nil
}

// manifestResponseEntry is the poll-ready form of a composed manifest: the
// exact bytes the response body carries, and the update's UUID for the
// same-version short-circuit.
type manifestResponseEntry struct {
	ManifestJSON json.RawMessage `json:"manifestJson"`
	UpdateUUID   string          `json:"updateUuid"`
}

// cachedManifestResponse is the single owner of the composed-manifest cache;
// the request path and the publish prewarm both go through it. Composing is
// what costs the store reads, so a warm poll never pays one.
func (s *UpdateService) cachedManifestResponse(ctx context.Context, update types.Update, platform types.Platform) (manifestResponseEntry, error) {
	manifestCache := cache.GetCache()
	cacheKey := update2.ComputeManifestResponseCacheKey(update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId, platform)
	if entry, ok := cache.GetJSON[manifestResponseEntry](manifestCache, cacheKey); ok {
		return entry, nil
	}
	flightEntry, err, _ := s.manifestFlight.Do(cacheKey, func() (any, error) {
		if entry, ok := cache.GetJSON[manifestResponseEntry](manifestCache, cacheKey); ok {
			return entry, nil
		}
		metadata, err := update2.GetMetadata(update)
		if err != nil {
			return nil, err
		}
		storedMetadata, err := s.updateRepo.RetrieveUpdateStoredMetadata(ctx, update)
		if err != nil {
			return nil, err
		}
		mapping, err := s.updateRepo.GetUpdateAssetMapping(ctx, update)
		if err != nil {
			return nil, err
		}
		manifest, err := update2.ComposeUpdateManifest(&metadata, update, storedMetadata, mapping, platform)
		if err != nil {
			return nil, err
		}
		manifestJSON, err := json.Marshal(manifest)
		if err != nil {
			return nil, err
		}
		entry := manifestResponseEntry{ManifestJSON: manifestJSON, UpdateUUID: manifest.Id}
		ttl := update2.ImmutableCacheTTLSeconds
		cache.SetJSON(manifestCache, cacheKey, entry, &ttl)
		return entry, nil
	})
	if err != nil {
		return manifestResponseEntry{}, err
	}
	return flightEntry.(manifestResponseEntry), nil
}

// GetUpdateAssetMapping answers nil for an update published before the mapping
// existed; composing then falls back to reading the update folder.
func (s *UpdateService) GetUpdateAssetMapping(ctx context.Context, update types.Update) (*types.UpdateAssetMapping, error) {
	return s.updateRepo.GetUpdateAssetMapping(ctx, update)
}

// RetrieveUpdateStoredMetadata exposes the repo read to callers that hold a
// service rather than the repository (the manifest prewarm).
func (s *UpdateService) RetrieveUpdateStoredMetadata(ctx context.Context, update types.Update) (*types.UpdateStoredMetadata, error) {
	return s.updateRepo.RetrieveUpdateStoredMetadata(ctx, update)
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
