package services

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

const (
	historyDownloadConcurrency = 8
	historyAssetCacheMaxBytes  = 64 << 20
	historyCacheableAssetSize  = 8 << 20
)

// skipUpdate is a per-update problem: it skips the update instead of failing the job.
type skipUpdate struct{ reason string }

func (e *skipUpdate) Error() string { return e.reason }

type branchRuntime struct {
	branch  string
	runtime string
}

// EAS asset URLs are content-addressed: equal URL means equal bytes.
type historyAssetCache struct {
	mu    sync.Mutex
	data  map[string][]byte
	bytes int
}

func newHistoryAssetCache() *historyAssetCache {
	return &historyAssetCache{data: map[string][]byte{}}
}

func (c *historyAssetCache) get(url string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.data[url]
	return data, ok
}

func (c *historyAssetCache) put(url string, data []byte) {
	if len(data) > historyCacheableAssetSize {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.data[url]; ok {
		return
	}
	if c.bytes+len(data) > historyAssetCacheMaxBytes {
		return
	}
	c.data[url] = data
	c.bytes += len(data)
}

// copyHistory copies the oldest fetched group first, so a stopped job leaves
// a consistent prefix of history.
func (s *ExpoImportService) copyHistory(ctx context.Context, appId string, tracker *jobs.Tracker, groups [][]expo.HistoryUpdate) error {
	touched := make(map[branchRuntime]bool)
	defer s.invalidateHistoryServingCaches(appId, touched)
	assetCache := newHistoryAssetCache()

	for groupIndex := len(groups) - 1; groupIndex >= 0; groupIndex-- {
		for _, historyUpdate := range groups[groupIndex] {
			skipReason, err := s.importHistoryUpdate(ctx, appId, historyUpdate, touched, assetCache)
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
func (s *ExpoImportService) importHistoryUpdate(ctx context.Context, appId string, historyUpdate expo.HistoryUpdate, touched map[branchRuntime]bool, assetCache *historyAssetCache) (string, error) {
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
	skipReason, err := s.writeHistoryUpdateFiles(ctx, update, platform, historyUpdate, served, assetCache)
	if err != nil {
		return "", err
	}
	if skipReason != "" {
		s.deleteHistoryUpdateFolder(update)
		return skipReason, nil
	}

	expoUpdateUUIDStr := expoUpdateUUID.String()
	params.UpdateType = types.NormalUpdate
	params.UpdateUUID = &expoUpdateUUIDStr
	inserted, err := s.updateRepo.ImportUpdate(ctx, params)
	if err != nil {
		// Without the row the files are unreachable orphans.
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

func (s *ExpoImportService) deleteHistoryUpdateFolder(update types.Update) {
	if err := s.bucket.DeleteUpdateFolder(update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId); err != nil {
		log.Printf("[expo-import] failed to clean up update folder %s: %v", update.UpdateId, err)
	}
}

// Download and integrity problems return a skip reason, bucket writes an error.
func (s *ExpoImportService) writeHistoryUpdateFiles(ctx context.Context, update types.Update, platform types.Platform, historyUpdate expo.HistoryUpdate, served *expo.ServedManifest, assetCache *historyAssetCache) (string, error) {
	manifest := &served.Manifest
	bundlePath := "bundles/" + string(platform) + "-" + historyAssetFileName(manifest.LaunchAsset, 0) + ".bundle"
	platformMetadata := types.PlatformMetadata{Bundle: bundlePath}

	type pendingAsset struct {
		asset expo.HistoryAsset
		path  string
		// Launch assets change on every publish: caching them only burns budget.
		cacheable bool
	}
	pending := []pendingAsset{{asset: manifest.LaunchAsset, path: bundlePath}}
	seenPaths := map[string]bool{bundlePath: true}
	for index, asset := range manifest.Assets {
		path := "assets/" + historyAssetFileName(asset, index)
		if seenPaths[path] {
			continue
		}
		seenPaths[path] = true
		pending = append(pending, pendingAsset{asset: asset, path: path, cacheable: true})
		platformMetadata.Assets = append(platformMetadata.Assets, types.Asset{
			Path: path,
			Ext:  strings.TrimPrefix(asset.FileExtension, "."),
		})
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(historyDownloadConcurrency)
	for _, entry := range pending {
		group.Go(func() error {
			if entry.asset.Url == "" {
				return &skipUpdate{reason: fmt.Sprintf("asset %q carries no download URL", entry.asset.Key)}
			}
			data, cached := assetCache.get(entry.asset.Url)
			if !cached {
				downloaded, err := expo.DownloadAsset(groupCtx, entry.asset.Url, served.AssetRequestHeaders[entry.asset.Key])
				if err != nil {
					return &skipUpdate{reason: err.Error()}
				}
				if entry.asset.Hash != "" {
					hash, hashErr := crypto.CreateHash(downloaded, "sha256", "base64")
					if hashErr != nil {
						return hashErr
					}
					if crypto.GetBase64URLEncoding(hash) != entry.asset.Hash {
						return &skipUpdate{reason: fmt.Sprintf("asset %q does not match its manifest hash", entry.asset.Key)}
					}
				}
				if entry.cacheable {
					assetCache.put(entry.asset.Url, downloaded)
				}
				data = downloaded
			}
			if err := s.bucket.UploadFileIntoUpdate(update, entry.path, bytes.NewReader(data)); err != nil {
				return fmt.Errorf("failed to write %s into the bucket: %w", entry.path, err)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		var skip *skipUpdate
		if errors.As(err, &skip) {
			return skip.reason, nil
		}
		return "", err
	}

	metadata := types.MetadataObject{Version: 0, Bundler: "metro"}
	if platform == types.PlatformIOS {
		metadata.FileMetadata.IOS = platformMetadata
	} else {
		metadata.FileMetadata.Android = platformMetadata
	}
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	if err := s.bucket.UploadFileIntoUpdate(update, "metadata.json", bytes.NewReader(metadataBytes)); err != nil {
		return "", fmt.Errorf("failed to write metadata.json into the bucket: %w", err)
	}

	if expoClient := manifest.ExpoClientConfig(); expoClient != nil {
		expoConfigBytes, err := json.Marshal(expoClient)
		if err != nil {
			return "", err
		}
		if err := s.bucket.UploadFileIntoUpdate(update, "expoConfig.json", bytes.NewReader(expoConfigBytes)); err != nil {
			return "", fmt.Errorf("failed to write expoConfig.json into the bucket: %w", err)
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
		return "", err
	}
	if err := s.bucket.UploadFileIntoUpdate(update, "update-metadata.json", bytes.NewReader(storedMetadataBytes)); err != nil {
		return "", fmt.Errorf("failed to write update-metadata.json into the bucket: %w", err)
	}
	return "", nil
}

func (s *ExpoImportService) invalidateHistoryServingCaches(appId string, touched map[branchRuntime]bool) {
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
