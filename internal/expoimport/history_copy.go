package expoimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"xprem/internal/bucket"
	cache2 "xprem/internal/cache"
	"xprem/internal/crypto"
	"xprem/internal/dashboard"
	"xprem/internal/jobs"
	"xprem/internal/providers/expo"
	"xprem/internal/store"
	"xprem/internal/types"
	update2 "xprem/internal/update"
	"xprem/internal/validation"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

const historyDownloadConcurrency = 8

// skipUpdate is a per-update problem: it skips the update instead of failing the job.
type skipUpdate struct{ reason string }

func (e *skipUpdate) Error() string { return e.reason }

type branchRuntime struct {
	branch  string
	runtime string
}

// copyHistory copies the oldest fetched group first, so a stopped job leaves
// a consistent prefix of history.
func (s *Service) copyHistory(ctx context.Context, appId string, tracker *jobs.Tracker, groups [][]expo.HistoryUpdate) error {
	touched := make(map[branchRuntime]bool)
	defer s.invalidateHistoryServingCaches(appId, touched)

	for groupIndex := len(groups) - 1; groupIndex >= 0; groupIndex-- {
		for _, historyUpdate := range groups[groupIndex] {
			skipReason, err := s.importHistoryUpdate(ctx, appId, historyUpdate, touched)
			// A canceled context outranks whatever the update reported.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				return fmt.Errorf("failed while importing update %s: %w", historyUpdate.Id, err)
			}
			if skipReason != "" {
				tracker.Skip(ctx, fmt.Sprintf("update %s (%s): %s", historyUpdate.Id, historyUpdate.Platform, skipReason))
			} else {
				tracker.Succeed(ctx)
			}
		}
	}
	return nil
}

// importHistoryUpdate returns a non-empty skip reason for problems scoped to
// this update, an error for failures that must stop the job.
func (s *Service) importHistoryUpdate(ctx context.Context, appId string, historyUpdate expo.HistoryUpdate, touched map[branchRuntime]bool) (string, error) {
	platform, ok := parseHistoryPlatform(historyUpdate.Platform)
	if !ok {
		return fmt.Sprintf("platform %q is not supported", historyUpdate.Platform), nil
	}
	if historyUpdate.BranchName == "" || historyUpdate.RuntimeVersion == "" {
		return "the update names no branch or no runtime version", nil
	}
	// Both names land in bucket paths: the same rule CreateBranch enforces applies.
	if err := validation.Name("branchName", historyUpdate.BranchName); err != nil {
		return fmt.Sprintf("branch %q: %s", historyUpdate.BranchName, validationMessage(err)), nil
	}
	if err := validation.Name("runtimeVersion", historyUpdate.RuntimeVersion); err != nil {
		return fmt.Sprintf("runtime version %q: %s", historyUpdate.RuntimeVersion, validationMessage(err)), nil
	}
	if historyUpdate.CodeSigned {
		return "signed with Expo code signing, its signature cannot be carried over", nil
	}
	createdAt, err := time.Parse(time.RFC3339, historyUpdate.CreatedAt)
	if err != nil {
		return fmt.Sprintf("unparseable publication date %q", historyUpdate.CreatedAt), nil
	}
	expoUpdateUUID, err := uuid.Parse(historyUpdate.Id)
	if err != nil {
		return fmt.Sprintf("unparseable update id %q", historyUpdate.Id), nil
	}

	// Same id scheme as GenerateUpdateTimestamp, but derived from the original
	// EAS publication instant so the branch timeline keeps its order.
	updateId := createdAt.UnixMilli()*10 + historyPlatformDigit(platform)
	// An occupied slot: already imported, or published locally at this exact
	// instant; writing this update's files would overwrite its own.
	occupied, err := s.updateRepo.UpdateExists(ctx, appId, historyUpdate.BranchName, updateId)
	if err != nil {
		return "", err
	}
	if occupied {
		return "an update already exists at this instant on this branch", nil
	}

	if err := s.branches.UpsertBranchAndRuntimeVersion(ctx, appId, historyUpdate.BranchName, historyUpdate.RuntimeVersion); err != nil {
		return "", fmt.Errorf("failed to upsert branch %q and runtime version %q: %w", historyUpdate.BranchName, historyUpdate.RuntimeVersion, err)
	}

	params := store.ImportUpdateParams{
		AppId:          appId,
		UpdateId:       updateId,
		BranchName:     historyUpdate.BranchName,
		RuntimeVersion: historyUpdate.RuntimeVersion,
		Platform:       platform,
		CommitHash:     historyUpdate.GitCommitHash,
		Message:        historyUpdate.Message,
		CreatedAt:      createdAt,
		CheckedAt:      createdAt,
	}
	if group, err := uuid.Parse(historyUpdate.Group); err == nil {
		groupStr := group.String()
		params.PublishGroup = &groupStr
	}

	if historyUpdate.IsRollBack {
		params.UpdateType = types.Rollback
		if _, err := s.updateRepo.ImportUpdate(ctx, params); err != nil {
			return "", err
		}
		touched[branchRuntime{branch: historyUpdate.BranchName, runtime: historyUpdate.RuntimeVersion}] = true
		return "", nil
	}

	if historyUpdate.ManifestPermalink == "" {
		return "the update carries no manifest permalink", nil
	}
	served, err := expo.FetchServedManifest(ctx, historyUpdate.ManifestPermalink)
	if err != nil {
		return err.Error(), nil
	}
	if served.Manifest.LaunchAsset.Url == "" {
		return "the manifest carries no launch asset", nil
	}

	update := types.Update{
		AppId:          appId,
		Branch:         historyUpdate.BranchName,
		RuntimeVersion: historyUpdate.RuntimeVersion,
		UpdateId:       strconv.FormatInt(updateId, 10),
	}
	// Blobs first: on a skip nothing has landed in the update folder yet, and
	// cas blobs are shared so they are never cleaned up.
	mapping, skipReason, err := s.storeHistoryBlobs(ctx, appId, served)
	if err != nil {
		return "", err
	}
	if skipReason != "" {
		return skipReason, nil
	}
	if err := s.writeHistoryConfigFiles(update, platform, historyUpdate, &served.Manifest); err != nil {
		s.deleteHistoryUpdateFolder(update)
		return "", err
	}

	expoUpdateUUIDStr := expoUpdateUUID.String()
	params.UpdateType = types.NormalUpdate
	params.UpdateUUID = &expoUpdateUUIDStr
	params.AssetMapping = mapping
	inserted, err := s.updateRepo.ImportUpdate(ctx, params)
	if err != nil {
		// Without the row the config files are unreachable orphans.
		s.deleteHistoryUpdateFolder(update)
		return "", err
	}
	touched[branchRuntime{branch: historyUpdate.BranchName, runtime: historyUpdate.RuntimeVersion}] = true
	if !inserted {
		log.Printf("[expo-import] timeline slot %s/%s already occupied; bucket files for update %s may have been overwritten", historyUpdate.BranchName, update.UpdateId, historyUpdate.Id)
		return "an update already exists at this instant on this branch; its files may have been overwritten", nil
	}
	return "", nil
}

func (s *Service) deleteHistoryUpdateFolder(update types.Update) {
	if err := s.bucket.DeleteUpdateFolder(update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId); err != nil {
		log.Printf("[expo-import] failed to clean up update folder %s: %v", update.UpdateId, err)
	}
}

// shapeHistoryAsset mirrors the publish-side shaping: the launch asset serves
// as .bundle whatever EAS called it.
func shapeHistoryAsset(asset expo.HistoryAsset, isLaunchAsset bool) types.ShapedAsset {
	shaped := types.ShapedAsset{
		Hash:          asset.Hash,
		Key:           asset.Key,
		FileExtension: asset.FileExtension,
		ContentType:   asset.ContentType,
	}
	if isLaunchAsset {
		shaped.FileExtension = ".bundle"
		if shaped.ContentType == "" {
			shaped.ContentType = "application/javascript"
		}
	}
	return shaped
}

// storeHistoryBlobs puts every asset of the served manifest in cas/ and
// returns the update's asset mapping. EAS hashes are base64url SHA-256, the
// exact cas key format, so a hash already in cas is not even downloaded.
// Download and integrity problems return a skip reason, cas writes an error.
func (s *Service) storeHistoryBlobs(ctx context.Context, appId string, served *expo.ServedManifest) (*types.UpdateAssetMapping, string, error) {
	manifest := &served.Manifest
	mapping := &types.UpdateAssetMapping{
		LaunchAsset: shapeHistoryAsset(manifest.LaunchAsset, true),
		Assets:      make([]types.ShapedAsset, len(manifest.Assets)),
	}
	sources := []expo.HistoryAsset{manifest.LaunchAsset}
	for i, asset := range manifest.Assets {
		mapping.Assets[i] = shapeHistoryAsset(asset, false)
		sources = append(sources, asset)
	}

	var mu sync.Mutex
	hashByURL := make(map[string]string, len(sources))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(historyDownloadConcurrency)
	seenURLs := make(map[string]bool, len(sources))
	for _, asset := range sources {
		if asset.Url == "" {
			return nil, fmt.Sprintf("asset %q carries no download URL", asset.Key), nil
		}
		if seenURLs[asset.Url] {
			continue
		}
		seenURLs[asset.Url] = true
		group.Go(func() error {
			hash, err := s.ensureHistoryBlob(groupCtx, appId, asset, served.AssetRequestHeaders[asset.Key])
			if err != nil {
				return err
			}
			mu.Lock()
			hashByURL[asset.Url] = hash
			mu.Unlock()
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		var skip *skipUpdate
		if errors.As(err, &skip) {
			return nil, skip.reason, nil
		}
		return nil, "", err
	}

	// An asset the manifest did not hash gets the one computed from its bytes.
	mapping.LaunchAsset.Hash = hashByURL[manifest.LaunchAsset.Url]
	for i, asset := range manifest.Assets {
		mapping.Assets[i].Hash = hashByURL[asset.Url]
	}
	return mapping, "", nil
}

// ensureHistoryBlob returns the cas hash of the asset, downloading and storing
// it only when the blob is not already there.
func (s *Service) ensureHistoryBlob(ctx context.Context, appId string, asset expo.HistoryAsset, headers map[string]string) (string, error) {
	declaredHash := asset.Hash
	if bucket.ValidateBlobHash(declaredHash) != nil {
		declaredHash = ""
	}
	if declaredHash != "" {
		exists, err := s.bucket.BlobExists(ctx, appId, declaredHash)
		if err != nil {
			return "", fmt.Errorf("failed to check cas for asset %q: %w", asset.Key, err)
		}
		if exists {
			return declaredHash, nil
		}
	}
	data, err := expo.DownloadAsset(ctx, asset.Url, headers)
	if err != nil {
		return "", &skipUpdate{reason: err.Error()}
	}
	hash, err := crypto.CreateHash(data, "sha256", "base64")
	if err != nil {
		return "", err
	}
	computedHash := crypto.GetBase64URLEncoding(hash)
	if declaredHash != "" && computedHash != declaredHash {
		return "", &skipUpdate{reason: fmt.Sprintf("asset %q does not match its manifest hash", asset.Key)}
	}
	if err := s.bucket.PutBlob(ctx, appId, computedHash, bytes.NewReader(data)); err != nil {
		return "", fmt.Errorf("failed to write asset %q into cas: %w", asset.Key, err)
	}
	return computedHash, nil
}

// writeHistoryConfigFiles writes the update folder's config files; the assets
// themselves live in cas/.
func (s *Service) writeHistoryConfigFiles(update types.Update, platform types.Platform, historyUpdate expo.HistoryUpdate, manifest *expo.HistoryManifest) error {
	platformMetadata := types.PlatformMetadata{
		Bundle: "bundles/" + string(platform) + "-" + historyAssetFileName(manifest.LaunchAsset, 0) + ".bundle",
	}
	seenPaths := map[string]bool{platformMetadata.Bundle: true}
	for index, asset := range manifest.Assets {
		path := "assets/" + historyAssetFileName(asset, index)
		if seenPaths[path] {
			continue
		}
		seenPaths[path] = true
		platformMetadata.Assets = append(platformMetadata.Assets, types.Asset{
			Path: path,
			Ext:  strings.TrimPrefix(asset.FileExtension, "."),
		})
	}

	metadata := types.MetadataObject{Version: 0, Bundler: "metro"}
	if platform == types.PlatformIOS {
		metadata.FileMetadata.IOS = platformMetadata
	} else {
		metadata.FileMetadata.Android = platformMetadata
	}
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	if err := s.bucket.UploadFileIntoUpdate(update, "metadata.json", bytes.NewReader(metadataBytes)); err != nil {
		return fmt.Errorf("failed to write metadata.json into the bucket: %w", err)
	}

	if expoClient := manifest.ExpoClientConfig(); expoClient != nil {
		expoConfigBytes, err := json.Marshal(expoClient)
		if err != nil {
			return err
		}
		if err := s.bucket.UploadFileIntoUpdate(update, "expoConfig.json", bytes.NewReader(expoConfigBytes)); err != nil {
			return fmt.Errorf("failed to write expoConfig.json into the bucket: %w", err)
		}
	}

	// The stored UUID is the original EAS update id: manifests keep the id
	// devices already know, so a migrated fleet does not re-download content
	// it is already running.
	storedMetadata := types.UpdateStoredMetadata{
		Platform:   platform,
		CommitHash: historyUpdate.GitCommitHash,
		UpdateUUID: historyUpdate.Id,
		Message:    historyUpdate.Message,
	}
	storedMetadataBytes, err := json.Marshal(storedMetadata)
	if err != nil {
		return err
	}
	if err := s.bucket.UploadFileIntoUpdate(update, "update-metadata.json", bytes.NewReader(storedMetadataBytes)); err != nil {
		return fmt.Errorf("failed to write update-metadata.json into the bucket: %w", err)
	}
	return nil
}

func (s *Service) invalidateHistoryServingCaches(appId string, touched map[branchRuntime]bool) {
	cache := cache2.GetCache()
	keys := []string{
		dashboard.ComputeGetBranchesCacheKey(appId),
		dashboard.ComputeGetChannelsCacheKey(appId),
	}
	for entry := range touched {
		keys = append(keys,
			dashboard.ComputeGetRuntimeVersionsCacheKey(appId, entry.branch),
			update2.ComputeLastUpdateCacheKey(appId, entry.branch, entry.runtime, types.PlatformIOS),
			update2.ComputeLastUpdateCacheKey(appId, entry.branch, entry.runtime, types.PlatformAndroid),
		)
	}
	for _, key := range keys {
		cache.Delete(key)
	}
}

func parseHistoryPlatform(platform string) (types.Platform, bool) {
	switch strings.ToLower(platform) {
	case string(types.PlatformIOS):
		return types.PlatformIOS, true
	case string(types.PlatformAndroid):
		return types.PlatformAndroid, true
	default:
		return "", false
	}
}

func historyPlatformDigit(platform types.Platform) int64 {
	if platform == types.PlatformIOS {
		return 1
	}
	return 2
}

var historyAssetKeyPattern = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func historyAssetFileName(asset expo.HistoryAsset, index int) string {
	name := historyAssetKeyPattern.ReplaceAllString(asset.Key, "")
	if strings.Trim(name, "._-") == "" {
		return fmt.Sprintf("asset-%d", index)
	}
	return name
}
