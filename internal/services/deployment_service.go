package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"xprem/internal/auditlog"
	"xprem/internal/bucket"
	"xprem/internal/cache"
	"xprem/internal/crypto"
	"xprem/internal/dashboard"
	"xprem/internal/database"
	"xprem/internal/store"
	"xprem/internal/types"
	update2 "xprem/internal/update"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

var (
	ErrInvalidUpdate     = errors.New("invalid update")
	ErrNoChangesDetected = errors.New("no changes detected in the update from the previous one")
	ErrInvalidBucketType = errors.New("the configured storage engine does not support local uploads")
	ErrInvalidToken      = errors.New("the provided upload token is invalid or expired")
	ErrTokenAppMismatch  = errors.New("upload token does not match the requested application context")
	ErrUploadFailed      = errors.New("failed to write upload file stream to destination storage")
	// ErrActiveRolloutBlocksPublish refuses any publish, republish or rollback on a
	// (branch, runtime version) that has an active per-update rollout.
	ErrActiveRolloutBlocksPublish = errors.New("a progressive rollout is active on this branch and runtime version; finish or revert it from the dashboard first")
	// ErrRolloutSuperseded refuses activation of a rollout update superseded by a
	// newer checked update on the same (branch, runtime version, platform).
	ErrRolloutSuperseded = errors.New("another update was published on this branch while this one was uploading; the rollout was not started, republish to retry")
	// ErrPublishGroupNotFound refuses a group operation whose target has no
	// checked member on this branch and runtime version.
	ErrPublishGroupNotFound = errors.New("no published updates found for this publish group on this branch and runtime version")
	// ErrLaunchAssetRequired refuses a publish whose file list names no bundle,
	// or names several: neither can produce a manifest.
	ErrLaunchAssetRequired = errors.New("expected exactly one launch asset in the file list")
)

type ProcessUpdateParams struct {
	RequestID      string
	AppID          string
	BranchName     string
	Platform       types.Platform
	RuntimeVersion string
	UpdateID       string
}

type RequestLocalFileUploadParams struct {
	RequestID  string
	AppID      string
	Token      string
	TokenAppID string
	FilePath   string
	Body       multipart.File
}

// FileRole is what a published file is to the update. Only the launch asset
// and the assets end up in the manifest; the config files are stored and
// served as-is.
type FileRole string

const (
	FileRoleLaunch FileRole = "launch"
	FileRoleAsset  FileRole = "asset"
	FileRoleConfig FileRole = "config"
)

// FileUploadItem is one file of one platform's publish. Key and Ext are the
// manifest data the CLI already computed; the server derives contentType and
// fileExtension from Ext so a single mime table decides them.
type FileUploadItem struct {
	Path string   `json:"path"`
	Hash string   `json:"hash"`
	Key  string   `json:"key,omitempty"`
	Ext  string   `json:"ext,omitempty"`
	Role FileRole `json:"role"`
}

// assetMapping is the manifest half of the file list: the roles the CLI stamped
// on each file are what says which platform's bundle this is, so nothing has to
// be inferred from a file name.
func assetMapping(files []FileUploadItem) (*types.UpdateAssetMapping, error) {
	mapping := types.UpdateAssetMapping{Assets: []types.ShapedAsset{}}
	launchAssets := 0
	for _, file := range files {
		switch file.Role {
		case FileRoleLaunch:
			mapping.LaunchAsset = shapeAsset(file, true)
			launchAssets++
		case FileRoleAsset:
			mapping.Assets = append(mapping.Assets, shapeAsset(file, false))
		}
	}
	if launchAssets != 1 {
		return nil, fmt.Errorf("%w: got %d", ErrLaunchAssetRequired, launchAssets)
	}
	return &mapping, nil
}

func shapeAsset(file FileUploadItem, isLaunchAsset bool) types.ShapedAsset {
	extension := file.Ext
	if isLaunchAsset {
		extension = "bundle"
	}
	return types.ShapedAsset{
		Hash:          file.Hash,
		Key:           file.Key,
		FileExtension: "." + extension,
		ContentType:   update2.AssetContentType(file.Ext, isLaunchAsset),
	}
}

type RequestUploadURLParams struct {
	RequestID      string
	AppID          string
	BranchName     string
	Platform       types.Platform
	CommitHash     string
	RuntimeVersion string
	// Files is one platform's publish: its launch asset, its assets, and the
	// config files. Never the other platform's.
	Files   []FileUploadItem
	Message string
	// Non-nil publishes the update as a progressive rollout served to this share
	// of devices (1-99).
	RolloutPercentage *int
	// Non-nil groups this update row with the other per-platform rows of the same
	// eoas run. Control-plane only: the bucket store ignores it.
	PublishGroupID *string
}

type RequestUploadURLResponse struct {
	UpdateID       int64
	UploadRequests []bucket.FileUploadRequest
}

type DeploymentService struct {
	branchService *BranchService
	updateService *UpdateService
	updateRepo    UpdateRepository
	bucket        bucket.Bucket
	// onAuditEvent is nil in community edition, where publishes, rollbacks and
	// republishes leave no events.
	onAuditEvent auditlog.RecordFunc
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *DeploymentService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// recordDeliveryEvent reports one delivery action on an update that just went live.
func (s *DeploymentService) recordDeliveryEvent(ctx context.Context, action auditlog.Action, update types.Update, metadata map[string]any) {
	if s.onAuditEvent == nil {
		return
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["branch"] = update.Branch
	metadata["runtime_version"] = update.RuntimeVersion
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        action,
		TargetType:    "update",
		TargetID:      update.UpdateId,
		TargetDisplay: update.UpdateId,
		AppID:         update.AppId,
		Metadata:      metadata,
	})
}

func NewDeploymentService(branchService *BranchService, updateService *UpdateService, updateRepo UpdateRepository, bucket bucket.Bucket) *DeploymentService {
	return &DeploymentService{
		branchService: branchService,
		updateService: updateService,
		updateRepo:    updateRepo,
		bucket:        bucket,
	}
}

func (s *DeploymentService) ProcessUploadedUpdate(ctx context.Context, params ProcessUpdateParams) error {

	err := s.branchService.UpsertBranchAndRuntimeVersion(ctx, params.AppID, params.BranchName, params.RuntimeVersion)
	if err != nil {
		log.Printf("[RequestID: %s] Error upserting branch and runtime version: %v", params.RequestID, err)
		return err
	}

	currentUpdate, err := s.updateRepo.GetUpdate(ctx, params.AppID, params.BranchName, params.RuntimeVersion, params.UpdateID)
	if err != nil {
		log.Printf("[RequestID: %s] Error getting update: %v", params.RequestID, err)
		return err
	}

	errorVerify := update2.VerifyUploadedUpdate(*currentUpdate)
	if errorVerify != nil {
		log.Printf("[RequestID: %s] Invalid update, deleting folder...", params.RequestID)
		err := s.bucket.DeleteUpdateFolder(params.AppID, params.BranchName, params.RuntimeVersion, params.UpdateID)
		if err != nil {
			log.Printf("[RequestID: %s] Error deleting update folder: %v", params.RequestID, err)
			return err
		}
		log.Printf("[RequestID: %s] Invalid update, folder deleted", params.RequestID)
		return fmt.Errorf("%w: %s", ErrInvalidUpdate, errorVerify)
	}

	err = s.MarkUpdateAsChecked(ctx, *currentUpdate, types.NormalUpdate)
	if err != nil {
		log.Printf("[RequestID: %s] Error marking update as checked: %v", params.RequestID, err)
		return err
	}
	log.Printf("[RequestID: %s] Update marked as checked", params.RequestID)
	s.recordDeliveryEvent(ctx, auditlog.ActionUpdatePublished, *currentUpdate,
		map[string]any{"platform": string(params.Platform)})
	return nil
}

func getUpdateUUIDFromMetadata(update types.Update) string {
	metadata, err := update2.GetMetadata(update)
	if err != nil {
		return ""
	}
	updateUUID := crypto.ConvertSHA256HashToUUID(metadata.ID)
	return updateUUID
}

func (s *DeploymentService) MarkUpdateAsChecked(ctx context.Context, update types.Update, updateType types.UpdateType) error {
	cache := cache.GetCache()
	branchesCacheKey := dashboard.ComputeGetBranchesCacheKey(update.AppId)
	channelsCacheKey := dashboard.ComputeGetChannelsCacheKey(update.AppId)
	runTimeVersionsCacheKey := dashboard.ComputeGetRuntimeVersionsCacheKey(update.AppId, update.Branch)
	storedMetadata, err := s.updateRepo.RetrieveUpdateStoredMetadata(ctx, update)
	if err != nil || storedMetadata == nil {
		return err
	}
	if updateType == types.NormalUpdate {
		// Rollbacks have no stored metadata to derive a UUID from.
		updateUUID := getUpdateUUIDFromMetadata(update)
		err = s.updateRepo.StoreUpdateUUIDInMetadata(ctx, update, updateUUID)
		if err != nil {
			return err
		}
	}
	// Must happen before the cache invalidation below, or a concurrent
	// /manifest request could re-cache the stale previous update.
	err = s.updateRepo.MarkUpdateAsChecked(ctx, update)
	if err != nil {
		if database.IsUniqueViolation(err) {
			return ErrActiveRolloutBlocksPublish
		}
		if errors.Is(err, store.ErrPublishBlockedByActiveRollout) {
			return ErrActiveRolloutBlocksPublish
		}
		if errors.Is(err, store.ErrRolloutSupersededByNewerUpdate) {
			return ErrRolloutSuperseded
		}
		return err
	}
	cacheKeys := []string{
		update2.ComputeLastUpdateCacheKey(update.AppId, update.Branch, update.RuntimeVersion, storedMetadata.Platform),
		branchesCacheKey,
		channelsCacheKey,
		runTimeVersionsCacheKey,
	}
	for _, cacheKey := range cacheKeys {
		cache.Delete(cacheKey)
	}
	go PreWarmManifestCache(s.updateService, update.AppId, update.Branch, update.RuntimeVersion, types.PlatformIOS)
	go PreWarmManifestCache(s.updateService, update.AppId, update.Branch, update.RuntimeVersion, types.PlatformAndroid)
	// No-op unless the checked update activated a per-update rollout.
	go PreWarmControlManifest(s.updateService, update.AppId, update.Branch, update.RuntimeVersion, types.PlatformIOS)
	go PreWarmControlManifest(s.updateService, update.AppId, update.Branch, update.RuntimeVersion, types.PlatformAndroid)
	return nil
}

func (s *DeploymentService) RequestUploadLocalFile(ctx context.Context, params RequestLocalFileUploadParams) error {
	bucketType := bucket.ResolveBucketType()
	if bucketType != bucket.LocalBucketType {
		log.Printf("[RequestID: %s] Invalid bucket type: %s", params.RequestID, bucketType)
		return ErrInvalidBucketType
	}

	// The token claim must match the app id on the URL, or a token leaked from
	// AppA could be replayed to write into AppB's bucket tree.
	if params.TokenAppID != params.AppID {
		log.Printf("[RequestID: %s] Token appId mismatch: token=%q url=%q", params.RequestID, params.TokenAppID, params.AppID)
		return ErrTokenAppMismatch
	}

	success, err := bucket.HandleUploadFile(params.FilePath, params.Body)
	if err != nil {
		log.Printf("[RequestID: %s] Error handling upload file: %v", params.RequestID, err)
		return err
	}
	if !success {
		log.Printf("[RequestID: %s] Error handling upload file", params.RequestID)
		return ErrUploadFailed
	}

	return nil
}

// dedupExistingUploadAssets returns the requested files that already exist in
// the cas folders so the caller can skip requesting uploads for them. Any
// failure falls back to uploading: the returned slice just omits the file.
func (s *DeploymentService) dedupExistingUploadAssets(ctx context.Context, params RequestUploadURLParams) []string {
	var dedupedAssets []string
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	// Files repeats assets shared by both platforms; copy each once.
	seen := make(map[string]struct{}, len(params.Files))
	for _, file := range params.Files {
		if _, alreadySeen := seen[file.Path]; alreadySeen {
			continue
		}
		seen[file.Path] = struct{}{}
		path := file.Path
		g.Go(func() error {
			exists, err := s.bucket.BlobExists(ctx, params.AppID, file.Hash)
			if err != nil {
				log.Printf("[RequestID: %s] Error while checking if blob exists: copying %s into update: %v", params.RequestID, path, err)
				return nil
			}
			if exists {
				dedupedAssets = append(dedupedAssets, path)
			}

			return nil
		})
	}
	_ = g.Wait()
	return dedupedAssets
}

// isIdenticalToLatest reports whether this publish would re-ship the content the
// branch already serves for the platform. A store that cannot answer never
// blocks the publish.
func (s *DeploymentService) isIdenticalToLatest(ctx context.Context, params RequestUploadURLParams, incoming *types.UpdateAssetMapping) bool {
	latest, err := s.updateService.GetLatestUpdate(ctx, params.AppID, params.BranchName, params.RuntimeVersion, params.Platform)
	if err != nil {
		log.Printf("[RequestID: %s] Warning: GetLatestUpdate returned error, skipping identical check: %v", params.RequestID, err)
		return false
	}
	if latest == nil {
		return false
	}
	stored, err := s.updateRepo.GetUpdateAssetMapping(ctx, *latest)
	if err != nil {
		log.Printf("[RequestID: %s] Warning: GetUpdateAssetMapping returned error, skipping identical check: %v", params.RequestID, err)
		return false
	}
	return update2.AreUpdatesIdentical(stored, incoming)
}

func (s *DeploymentService) RequestUploadURLs(ctx context.Context, params RequestUploadURLParams) (*RequestUploadURLResponse, error) {
	err := s.branchService.UpsertBranchAndRuntimeVersion(ctx, params.AppID, params.BranchName, params.RuntimeVersion)
	if err != nil {
		log.Printf("[RequestID: %s] Error upserting branch and runtime version: %v", params.RequestID, err)
		return nil, err
	}

	hasActiveRollout, err := s.updateRepo.HasActiveRolloutUpdate(ctx, params.AppID, params.BranchName, params.RuntimeVersion)
	if err != nil {
		log.Printf("[RequestID: %s] Error checking active rollout state: %v", params.RequestID, err)
		return nil, err
	}
	if hasActiveRollout {
		log.Printf("[RequestID: %s] Publish blocked: active rollout on branch %s (runtime version %s)", params.RequestID, params.BranchName, params.RuntimeVersion)
		return nil, ErrActiveRolloutBlocksPublish
	}

	// Refused here rather than at markUpdateAsUploaded, where the same publish
	// would die on the missing bundle after the CLI uploaded everything.
	mapping, err := assetMapping(params.Files)
	if err != nil {
		log.Printf("[RequestID: %s] %v", params.RequestID, err)
		return nil, err
	}
	if s.isIdenticalToLatest(ctx, params, mapping) {
		log.Printf("[RequestID: %s] Updates are identical, refusing upload", params.RequestID)
		return nil, ErrNoChangesDetected
	}

	updateId := update2.GenerateUpdateTimestamp(params.Platform)

	dedupedAssets := s.dedupExistingUploadAssets(ctx, params)

	filesToUpload := params.Files
	if len(dedupedAssets) > 0 {
		log.Printf("[RequestID: %s] Reusing %d unchanged assets from the previous update", params.RequestID, len(dedupedAssets))
		filesToUpload = slices.DeleteFunc(slices.Clone(params.Files), func(file FileUploadItem) bool {
			return slices.Contains(dedupedAssets, file.Path)
		})
	}

	files := make([]bucket.UploadFile, 0, len(filesToUpload))
	for _, file := range filesToUpload {
		files = append(files, bucket.UploadFile{Name: file.Path, Hash: file.Hash})
	}
	updateRequests, err := bucket.RequestUploadUrlsForFileUpdates(
		params.AppID,
		params.BranchName,
		files,
	)
	if err != nil {
		log.Printf("[RequestID: %s] Error requesting upload urls: %v", params.RequestID, err)
		return nil, err
	}

	var newUpdate *types.Update
	if params.RolloutPercentage != nil {
		newUpdate, err = s.updateRepo.CreateUpdateWithRollout(
			ctx,
			params.AppID,
			updateId,
			params.BranchName,
			params.RuntimeVersion,
			params.Platform,
			params.CommitHash,
			params.Message,
			*params.RolloutPercentage,
			params.PublishGroupID,
		)
	} else {
		newUpdate, err = s.updateRepo.CreateUpdate(
			ctx,
			params.AppID,
			updateId,
			params.BranchName,
			params.RuntimeVersion,
			params.Platform,
			params.CommitHash,
			params.Message,
			params.PublishGroupID,
		)
	}
	if err != nil {
		log.Printf("[RequestID: %s] Error uploading file update metadata: %v", params.RequestID, err)
		return nil, err
	}
	if newUpdate == nil {
		log.Printf("[RequestID: %s] Error creating update record: no update returned", params.RequestID)
		return nil, fmt.Errorf("failed to create update record: no update returned")
	}
	// Stored before the blobs it names exist. Every reader of a mapping goes
	// through GetLatestUpdate, which only ever answers a checked update, so an
	// abandoned publish leaves a mapping nobody can reach.
	if err := s.updateRepo.StoreUpdateAssetMapping(ctx, *newUpdate, mapping); err != nil {
		log.Printf("[RequestID: %s] Error storing update asset mapping: %v", params.RequestID, err)
		return nil, err
	}
	updateIdInt, _ := strconv.ParseInt(newUpdate.UpdateId, 10, 64)

	return &RequestUploadURLResponse{
		UpdateID:       updateIdInt,
		UploadRequests: updateRequests,
	}, nil
}

func (s *DeploymentService) CreateRollback(ctx context.Context, appId string, platform types.Platform, commitHash, runtimeVersion, branchName, message string) (*types.Update, error) {
	hasActiveRollout, err := s.updateRepo.HasActiveRolloutUpdate(ctx, appId, branchName, runtimeVersion)
	if err != nil {
		return nil, err
	}
	if hasActiveRollout {
		return nil, ErrActiveRolloutBlocksPublish
	}
	rollback, err := s.createRollbackInternal(ctx, appId, platform, commitHash, runtimeVersion, branchName, message)
	if err != nil {
		return nil, err
	}
	metadata := map[string]any{"platform": string(platform), "commit_hash": commitHash}
	if message != "" {
		metadata["message"] = message
	}
	s.recordDeliveryEvent(ctx, auditlog.ActionUpdateRollback, *rollback, metadata)
	return rollback, nil
}

// RepublishUpdateByID republishes a single update the caller names by id only,
// reading its platform and commit hash back from the stored update rather than
// from the request.
func (s *DeploymentService) RepublishUpdateByID(ctx context.Context, appId, branchName, runtimeVersion, updateId string) (*types.Update, error) {
	// A nil result (not an error) means the update wasn't found; a non-nil
	// error is always infrastructure and travels unwrapped as a 500.
	previousUpdate, err := s.updateRepo.GetUpdate(ctx, appId, branchName, runtimeVersion, updateId)
	if err != nil {
		return nil, fmt.Errorf("failed to read the update to republish: %w", err)
	}
	if previousUpdate == nil {
		return nil, &RepublishError{Status: http.StatusNotFound, Message: "No update found"}
	}
	metadata, err := s.updateRepo.RetrieveUpdateStoredMetadata(ctx, *previousUpdate)
	if err != nil {
		return nil, fmt.Errorf("failed to read the stored metadata of the update to republish: %w", err)
	}
	if metadata == nil {
		return nil, &RepublishError{Status: http.StatusNotFound, Message: "No stored metadata found for update"}
	}
	return s.RepublishUpdate(ctx, previousUpdate, metadata.Platform, metadata.CommitHash, nil)
}

// GroupOperationResult carries the outcome of a publish-group-wide republish:
// the server-minted group shared by the created rows, and the rows themselves.
type GroupOperationResult struct {
	PublishGroup string
	Updates      []types.Update
}

// RepublishPublishGroup republishes every member of one publish group, each on
// its own platform, under a new server-minted group.
func (s *DeploymentService) RepublishPublishGroup(ctx context.Context, appId, branchName, runtimeVersion, publishGroup string) (*GroupOperationResult, error) {
	members, err := s.updateRepo.GetUpdatesByPublishGroup(ctx, appId, branchName, runtimeVersion, publishGroup)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, ErrPublishGroupNotFound
	}
	result := &GroupOperationResult{PublishGroup: uuid.NewString()}
	for _, member := range members {
		previousUpdate := &types.Update{
			AppId:          appId,
			Branch:         branchName,
			RuntimeVersion: runtimeVersion,
			UpdateId:       member.UpdateId,
		}
		newUpdate, err := s.RepublishUpdate(ctx, previousUpdate, member.Platform, member.CommitHash, &result.PublishGroup)
		if err != nil {
			return nil, fmt.Errorf("republish of platform %s failed: %w", member.Platform, err)
		}
		result.Updates = append(result.Updates, *newUpdate)
	}
	return result, nil
}

// createRollbackInternal is CreateRollback without the active-rollout guard; the
// guard-free path exists for RolloutService, whose revert legitimately writes while
// the rollout is still active.
func (s *DeploymentService) createRollbackInternal(ctx context.Context, appId string, platform types.Platform, commitHash, runtimeVersion, branchName, message string) (*types.Update, error) {
	updateId := update2.GenerateUpdateTimestamp(platform)
	rollback, err := s.updateRepo.CreateRollback(ctx, appId, updateId, branchName, runtimeVersion, platform, commitHash, message)
	if err != nil {
		return nil, err
	}
	if rollback == nil {
		return nil, fmt.Errorf("failed to create rollback: no update returned")
	}
	err = s.MarkUpdateAsChecked(ctx, *rollback, types.Rollback)
	if err != nil {
		return nil, err
	}
	return rollback, nil
}

// RepublishError rejects a republish request because the source update is
// unusable: missing, a rollback, incomplete, or built for another platform.
// It carries the HTTP status the handler should surface.
type RepublishError struct {
	Status  int
	Message string
}

func (e *RepublishError) Error() string { return e.Message }

func (s *DeploymentService) RepublishUpdate(ctx context.Context, previousUpdate *types.Update, platform types.Platform, commitHash string, publishGroup *string) (*types.Update, error) {
	hasActiveRollout, err := s.updateRepo.HasActiveRolloutUpdate(ctx, previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion)
	if err != nil {
		return nil, err
	}
	if hasActiveRollout {
		return nil, ErrActiveRolloutBlocksPublish
	}
	newUpdate, err := s.republishUpdateInternal(ctx, previousUpdate, platform, commitHash, publishGroup)
	if err != nil {
		return nil, err
	}
	s.recordDeliveryEvent(ctx, auditlog.ActionUpdateRepublished, *newUpdate,
		map[string]any{"platform": string(platform), "source_update_id": previousUpdate.UpdateId})
	return newUpdate, nil
}

// republishUpdateInternal is RepublishUpdate without the active-rollout guard.
func (s *DeploymentService) republishUpdateInternal(ctx context.Context, previousUpdate *types.Update, platform types.Platform, commitHash string, publishGroup *string) (*types.Update, error) {
	existing, err := s.updateRepo.GetUpdate(ctx, previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion, previousUpdate.UpdateId)
	if err != nil {
		return nil, &RepublishError{Status: http.StatusBadRequest, Message: "Error getting update"}
	}
	if existing == nil {
		return nil, &RepublishError{Status: http.StatusNotFound, Message: "No update found"}
	}
	updateType, err := s.updateRepo.GetUpdateType(ctx, *existing)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve update type: %w", err)
	}
	if updateType != types.NormalUpdate {
		return nil, &RepublishError{Status: http.StatusBadRequest, Message: "Update type is not normal update"}
	}
	valid, err := s.updateRepo.IsUpdateValid(ctx, *existing)
	if err != nil {
		return nil, fmt.Errorf("failed to check update validity: %w", err)
	}
	if !valid {
		return nil, &RepublishError{Status: http.StatusBadRequest, Message: "Update is not valid"}
	}
	metadata, err := s.updateRepo.RetrieveUpdateStoredMetadata(ctx, *existing)
	if err != nil {
		return nil, &RepublishError{Status: http.StatusInternalServerError, Message: "Error retrieving update commit hash and platform"}
	}
	if metadata == nil {
		return nil, &RepublishError{Status: http.StatusNotFound, Message: "No stored metadata found for update"}
	}
	if metadata.Platform != platform {
		return nil, &RepublishError{Status: http.StatusBadRequest, Message: "Update platform mismatch"}
	}

	updateId := update2.GenerateUpdateTimestamp(platform)
	_, err = s.bucket.CreateUpdateFrom(previousUpdate, update2.ConvertUpdateTimestampToString(updateId))
	if err != nil {
		return nil, err
	}
	newUpdate, err := s.updateRepo.CreateUpdate(ctx, previousUpdate.AppId, updateId, previousUpdate.Branch, previousUpdate.RuntimeVersion, platform, commitHash, "", publishGroup)
	if err != nil {
		return nil, err
	}
	err = s.MarkUpdateAsChecked(ctx, *newUpdate, types.NormalUpdate)
	if err != nil {
		return nil, err
	}
	return newUpdate, nil
}
