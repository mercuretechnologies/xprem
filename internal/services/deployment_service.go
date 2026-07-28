package services

import (
	"context"
	"errors"
	"expo-open-ota/internal/auditlog"
	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/crypto"
	"expo-open-ota/internal/dashboard"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/store"
	"expo-open-ota/internal/types"
	update2 "expo-open-ota/internal/update"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"

	"github.com/google/uuid"
)

var (
	ErrInvalidUpdate     = errors.New("invalid update")
	ErrNoChangesDetected = errors.New("no changes detected in the update from the previous one")
	ErrInvalidBucketType = errors.New("the configured storage engine does not support local uploads")
	ErrInvalidToken      = errors.New("the provided upload token is invalid or expired")
	ErrTokenAppMismatch  = errors.New("upload token does not match the requested application context")
	ErrUploadFailed      = errors.New("failed to write upload file stream to destination storage")
	// ErrActiveRolloutBlocksPublish refuses any publish, republish or rollback on a
	// (branch, runtime version) that has an active per-update rollout. Handlers map it
	// to a 409; RolloutService bypasses it through the unexported internal helpers.
	ErrActiveRolloutBlocksPublish = errors.New("a progressive rollout is active on this branch and runtime version; finish or revert it from the dashboard first")
	// ErrRolloutSuperseded refuses the activation of a rollout update when a newer
	// checked update landed on the same (branch, runtime version, platform) during
	// its upload: activating it would advertise a rollout that is never served.
	ErrRolloutSuperseded = errors.New("another update was published on this branch while this one was uploading; the rollout was not started, republish to retry")
	// ErrPublishGroupNotFound refuses a group operation whose target has no
	// checked member on this branch and runtime version. Handlers map it to 404.
	ErrPublishGroupNotFound = errors.New("no published updates found for this publish group on this branch and runtime version")
)

type ProcessUpdateParams struct {
	RequestID      string
	AppID          string
	BranchName     string
	Platform       string
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

type RequestUploadURLParams struct {
	RequestID      string
	AppID          string
	BranchName     string
	Platform       string
	CommitHash     string
	RuntimeVersion string
	FileNames      []string
	Message        string
	// Non-nil publishes the update as a progressive rollout served to this share of
	// devices (1-99, validated by the handler, which also requires a platform).
	RolloutPercentage *int
	// Non-nil groups this update row with the other per-platform rows of the
	// same eoas run (CLI-minted UUID, validated by the handler). Control-plane
	// only: the bucket store ignores it.
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
	// onAuditEvent is the audit emission seam; nil (community) means
	// publishes, rollbacks and republishes leave no events.
	onAuditEvent auditlog.RecordFunc
}

// SetOnAuditEvent plugs the audit emission seam (see SetSSOEnforced for the
// pattern). Nil-safe.
func (s *DeploymentService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// recordDeliveryEvent reports one delivery action on an update that just went
// live. The actor comes from the request context: the CLI credential of the
// publish routes, or the dashboard principal.
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
		// Delete folder and throw error
		log.Printf("[RequestID: %s] Invalid update, deleting folder...", params.RequestID)
		err := s.bucket.DeleteUpdateFolder(params.AppID, params.BranchName, params.RuntimeVersion, params.UpdateID)
		if err != nil {
			log.Printf("[RequestID: %s] Error deleting update folder: %v", params.RequestID, err)
			return err
		}
		log.Printf("[RequestID: %s] Invalid update, folder deleted", params.RequestID)
		return fmt.Errorf("%w: %s", ErrInvalidUpdate, errorVerify)
	}
	// Now we have to retrieve the latest update and compare hash changes
	latestUpdate, err := s.updateService.GetLatestUpdate(ctx, params.AppID, params.BranchName, params.RuntimeVersion, params.Platform)
	shouldMarkAsChecked := false

	if err != nil {
		log.Printf("[RequestID: %s] Warning: store.GetLatestUpdate returned error, falling back to checked state: %v", params.RequestID, err)
		shouldMarkAsChecked = true
	} else if latestUpdate == nil {
		log.Printf("[RequestID: %s] No latest update found, marking current update as checked", params.RequestID)
		shouldMarkAsChecked = true
	}

	if shouldMarkAsChecked {
		err = s.MarkUpdateAsChecked(ctx, *currentUpdate, types.NormalUpdate)
		if err != nil {
			log.Printf("[RequestID: %s] Error marking update as checked: %v", params.RequestID, err)
			return err
		}
		log.Printf("[RequestID: %s] Latest update evaluation triggered auto-check routine.", params.RequestID)
		s.recordDeliveryEvent(ctx, auditlog.ActionUpdatePublished, *currentUpdate,
			map[string]any{"platform": params.Platform})
		return nil
	}

	areUpdatesIdentical, err := update2.AreUpdatesIdentical(*currentUpdate, *latestUpdate)
	if err != nil {
		log.Printf("[RequestID: %s] Error comparing updates: %v", params.RequestID, err)
		return err
	}

	if !areUpdatesIdentical {
		err = s.MarkUpdateAsChecked(ctx, *currentUpdate, types.NormalUpdate)
		if err != nil {
			log.Printf("[RequestID: %s] Error marking update as checked: %v", params.RequestID, err)
			return err
		}
		log.Printf("[RequestID: %s] Updates are not identical, update marked as checked", params.RequestID)
		s.recordDeliveryEvent(ctx, auditlog.ActionUpdatePublished, *currentUpdate,
			map[string]any{"platform": params.Platform})
		return nil
	}

	log.Printf("[RequestID: %s] Updates are identical, delete folder...", params.RequestID)
	err = s.bucket.DeleteUpdateFolder(params.AppID, params.BranchName, params.RuntimeVersion, currentUpdate.UpdateId)
	if err != nil {
		log.Printf("[RequestID: %s] Error deleting update folder: %v", params.RequestID, err)
		return err
	}
	log.Printf("[RequestID: %s] Updates are identical, folder deleted", params.RequestID)

	return ErrNoChangesDetected
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
	updatesCacheKey := dashboard.ComputeGetUpdatesCacheKey(update.AppId, update.Branch, update.RuntimeVersion)
	storedMetadata, err := s.updateRepo.RetrieveUpdateStoredMetadata(ctx, update)
	if err != nil || storedMetadata == nil {
		return err
	}
	// Retrieve the update UUID only for normal updates, as rollbacks don't have metadata stored
	if updateType == types.NormalUpdate {
		updateUUID := getUpdateUUIDFromMetadata(update)
		err = s.updateRepo.StoreUpdateUUIDInMetadata(ctx, update, updateUUID)
		if err != nil {
			return err
		}
	}
	// Mark update as checked BEFORE invalidating the lastUpdate cache.
	// The check on valid update uses .check as the "this update is complete
	// and pickable" sentinel;
	// if we deleted the cache first, a concurrent /manifest request would
	// miss, re-scan updates, find this one without .check, filter it out,
	// and re-cache the previous update for the full TTL (1800s) — serving
	// a stale manifest for up to 30 minutes after a publish or rollback.
	err = s.updateRepo.MarkUpdateAsChecked(ctx, update)
	if err != nil {
		// The partial unique index on active rollout rows is the transactional
		// close of the publish race: a second rollout update reaching checked
		// state on the same (branch, rtv, platform) violates it here.
		if database.IsUniqueViolation(err) {
			return ErrActiveRolloutBlocksPublish
		}
		// The conditional stamp covers the other two race directions: a plain
		// update racing an in-flight rollout activation, and a rollout update
		// superseded by a newer publish during its upload.
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
		updatesCacheKey,
	}
	for _, cacheKey := range cacheKeys {
		cache.Delete(cacheKey)
	}
	go PreWarmManifestCache(s.updateService, update.AppId, update.Branch, update.RuntimeVersion, "ios")
	go PreWarmManifestCache(s.updateService, update.AppId, update.Branch, update.RuntimeVersion, "android")
	// When the checked update activated a per-update rollout, its out-of-bucket
	// cohort is served the control update: warm that manifest too, or the first
	// such client re-hashes every control asset. No-op without an active rollout.
	go PreWarmControlManifest(s.updateService, update.AppId, update.Branch, update.RuntimeVersion, "ios")
	go PreWarmControlManifest(s.updateService, update.AppId, update.Branch, update.RuntimeVersion, "android")
	return nil
}

func (s *DeploymentService) RequestUploadLocalFile(ctx context.Context, params RequestLocalFileUploadParams) error {
	bucketType := bucket.ResolveBucketType()
	if bucketType != bucket.LocalBucketType {
		log.Printf("[RequestID: %s] Invalid bucket type: %s", params.RequestID, bucketType)
		return ErrInvalidBucketType
	}

	// Defense against a leaked-token cross-app write: the token claim must
	// match the app id on the URL. Without this check, a valid token scoped
	// to AppA could be replayed via /{AppB}/uploadLocalFile to land bytes
	// under AppA's bucket tree from an AppB-authenticated session.
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

func (s *DeploymentService) RequestUploadURLs(ctx context.Context, params RequestUploadURLParams) (*RequestUploadURLResponse, error) {
	err := s.branchService.UpsertBranchAndRuntimeVersion(ctx, params.AppID, params.BranchName, params.RuntimeVersion)
	if err != nil {
		log.Printf("[RequestID: %s] Error upserting branch and runtime version: %v", params.RequestID, err)
		return nil, err
	}

	// Fail fast on every publish while a per-update rollout is active; the partial
	// unique index closes the remaining race at MarkUpdateAsChecked time.
	hasActiveRollout, err := s.updateRepo.HasActiveRolloutUpdate(ctx, params.AppID, params.BranchName, params.RuntimeVersion)
	if err != nil {
		log.Printf("[RequestID: %s] Error checking active rollout state: %v", params.RequestID, err)
		return nil, err
	}
	if hasActiveRollout {
		log.Printf("[RequestID: %s] Publish blocked: active rollout on branch %s (runtime version %s)", params.RequestID, params.BranchName, params.RuntimeVersion)
		return nil, ErrActiveRolloutBlocksPublish
	}

	updateId := update2.GenerateUpdateTimestamp(params.Platform)
	updateStr := update2.ConvertUpdateTimestampToString(updateId)

	updateRequests, err := bucket.RequestUploadUrlsForFileUpdates(
		params.AppID,
		params.BranchName,
		params.RuntimeVersion,
		updateStr,
		params.FileNames,
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
	// newUpdate.UpdateId is already a string, no need to format
	updateIdInt, _ := strconv.ParseInt(newUpdate.UpdateId, 10, 64)

	return &RequestUploadURLResponse{
		UpdateID:       updateIdInt,
		UploadRequests: updateRequests,
	}, nil
}

func (s *DeploymentService) CreateRollback(ctx context.Context, appId, platform, commitHash, runtimeVersion, branchName, message string) (*types.Update, error) {
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
	// Only the caller-facing rollback records here: RolloutService's revert goes
	// through the internal variant and reports as update_rollout.reverted.
	metadata := map[string]any{"platform": platform, "commit_hash": commitHash}
	// Absent rather than empty when the caller gave none (the CLI), so the
	// entries of a path that can never carry one stay free of a dead key.
	if message != "" {
		metadata["message"] = message
	}
	s.recordDeliveryEvent(ctx, auditlog.ActionUpdateRollback, *rollback, metadata)
	return rollback, nil
}

// RepublishUpdateByID republishes a single update the caller names by id only.
// Where the CLI path stamps the run's own platform and commit hash, both are
// read back from the stored update here: the dashboard has no working copy, a
// republish is by definition the same code as its source, and a platform taken
// from the request body would only ever be the source's own (the service
// refuses a mismatch) or a 400.
//
// Works on both backends: the metadata read goes through the repo, so it is the
// updates row in control-plane mode and update-metadata.json in stateless mode.
func (s *DeploymentService) RepublishUpdateByID(ctx context.Context, appId, branchName, runtimeVersion, updateId string) (*types.Update, error) {
	previousUpdate, err := s.updateRepo.GetUpdate(ctx, appId, branchName, runtimeVersion, updateId)
	if err != nil {
		return nil, &RepublishError{Status: http.StatusBadRequest, Message: "Error getting update"}
	}
	if previousUpdate == nil {
		return nil, &RepublishError{Status: http.StatusNotFound, Message: "No update found"}
	}
	metadata, err := s.updateRepo.RetrieveUpdateStoredMetadata(ctx, *previousUpdate)
	if err != nil || metadata == nil {
		return nil, &RepublishError{Status: http.StatusNotFound, Message: "No stored metadata found for update"}
	}
	return s.RepublishUpdate(ctx, previousUpdate, metadata.Platform, metadata.CommitHash, nil)
}

// GroupOperationResult carries the outcome of a publish-group-wide republish:
// the server-minted group shared by the created rows, and the rows themselves
// (one per acted-on member).
type GroupOperationResult struct {
	PublishGroup string
	Updates      []types.Update
}

// RepublishPublishGroup republishes every member of one publish group on its
// own platform, the new rows sharing a new server-minted group. A group of
// rollback markers is refused member by member through the same validation as
// a single republish. Fails fast on the first platform error; rows already
// created by the run stay, the same partial-completion contract as a
// per-platform CLI loop.
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
func (s *DeploymentService) createRollbackInternal(ctx context.Context, appId, platform, commitHash, runtimeVersion, branchName, message string) (*types.Update, error) {
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
// unusable — missing, a rollback, incomplete, or built for another platform.
// It carries the HTTP status the handler should surface.
type RepublishError struct {
	Status  int
	Message string
}

func (e *RepublishError) Error() string { return e.Message }

func (s *DeploymentService) RepublishUpdate(ctx context.Context, previousUpdate *types.Update, platform, commitHash string, publishGroup *string) (*types.Update, error) {
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
	// Same split as CreateRollback: the rollout revert republishes through
	// the internal variant and reports as update_rollout.reverted.
	s.recordDeliveryEvent(ctx, auditlog.ActionUpdateRepublished, *newUpdate,
		map[string]any{"platform": platform, "source_update_id": previousUpdate.UpdateId})
	return newUpdate, nil
}

// republishUpdateInternal is RepublishUpdate without the active-rollout guard; see
// createRollbackInternal.
func (s *DeploymentService) republishUpdateInternal(ctx context.Context, previousUpdate *types.Update, platform, commitHash string, publishGroup *string) (*types.Update, error) {
	// Validate the source update before cloning it. Done through the injected
	// repo so it is correct on both backends: type/metadata/validity come from
	// Postgres on the DB control plane and from the stored files on the bucket
	// backend. (This can't live in the handler against the global update.*
	// helpers — those only ever read the bucket, so a DB-mode rollback would
	// slip through.)
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
