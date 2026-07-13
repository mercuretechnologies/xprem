package update

import (
	"encoding/json"
	"expo-open-ota/config"
	"expo-open-ota/internal/bucket"
	cache2 "expo-open-ota/internal/cache"
	"expo-open-ota/internal/crypto"
	"expo-open-ota/internal/dashboard"
	"expo-open-ota/internal/types"
	"expo-open-ota/internal/version"
	"fmt"
	"mime"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func sortUpdates(updates []types.Update) []types.Update {
	sort.Slice(updates, func(i, j int) bool {
		return updates[i].CreatedAt > updates[j].CreatedAt
	})
	return updates
}

func filterPlatformUpdates(updates []types.Update, platform string) []types.Update {
	filteredUpdates := make([]types.Update, 0)
	for _, update := range updates {
		storedMetadata, err := RetrieveUpdateStoredMetadata(update)
		if err == nil && storedMetadata != nil && storedMetadata.Platform == platform {
			filteredUpdates = append(filteredUpdates, update)
		}
	}
	return filteredUpdates
}

func GetAllUpdatesForRuntimeVersion(branch string, runtimeVersion string, platform string) ([]types.Update, error) {
	resolvedBucket := bucket.GetBucket()
	updates, errGetUpdates := resolvedBucket.GetUpdates(branch, runtimeVersion)
	if errGetUpdates != nil {
		return nil, errGetUpdates
	}
	updates = sortUpdates(filterPlatformUpdates(updates, platform))
	return updates, nil
}

func StoreUpdateUUIDInMetadata(update types.Update) error {
	resolvedBucket := bucket.GetBucket()
	file, err := resolvedBucket.GetFile(update, "update-metadata.json")
	if err != nil {
		return err
	}
	defer file.Reader.Close()
	var storedMetadata types.UpdateStoredMetadata
	err = json.NewDecoder(file.Reader).Decode(&storedMetadata)
	if err != nil {
		return err
	}
	metadata, err := GetMetadata(update)
	if err != nil {
		return err
	}
	storedMetadata.UpdateUUID = crypto.ConvertSHA256HashToUUID(metadata.ID)
	updatedMetadata, err := json.Marshal(storedMetadata)
	if err != nil {
		return err
	}
	reader := strings.NewReader(string(updatedMetadata))
	err = resolvedBucket.UploadFileIntoUpdate(update, "update-metadata.json", reader)
	if err != nil {
		return err
	}
	return nil
}

func MarkUpdateAsChecked(update types.Update) error {
	cache := cache2.GetCache()
	branchesCacheKey := dashboard.ComputeGetBranchesCacheKey()
	runTimeVersionsCacheKey := dashboard.ComputeGetRuntimeVersionsCacheKey(update.Branch)
	updatesCacheKey := dashboard.ComputeGetUpdatesCacheKey(update.Branch, update.RuntimeVersion)
	storedMetadata, err := RetrieveUpdateStoredMetadata(update)
	if err != nil || storedMetadata == nil {
		return err
	}
	cacheKeys := []string{ComputeLastUpdateCacheKey(update.Branch, update.RuntimeVersion, storedMetadata.Platform), branchesCacheKey, runTimeVersionsCacheKey, updatesCacheKey}
	for _, cacheKey := range cacheKeys {
		cache.Delete(cacheKey)
	}
	resolvedBucket := bucket.GetBucket()
	err = StoreUpdateUUIDInMetadata(update)
	if err != nil {
		return err
	}
	reader := strings.NewReader(".check")
	_ = resolvedBucket.UploadFileIntoUpdate(update, ".check", reader)
	go PreWarmUpdateManifestCache(update, storedMetadata.Platform)
	return nil
}

func IsUpdateValid(Update types.Update) bool {
	resolvedBucket := bucket.GetBucket()
	// Search for .check file in the update
	file, _ := resolvedBucket.GetFile(Update, ".check")
	if file != nil {
		file.Reader.Close()
		return true
	}
	return false
}

func ComputeLastUpdateCacheKey(branch string, runtimeVersion string, platform string) string {
	return fmt.Sprintf("lastUpdate:%s:%s:%s:%s", version.Version, branch, runtimeVersion, platform)
}

func ComputeMetadataCacheKey(branch string, runtimeVersion string, updateId string) string {
	return fmt.Sprintf("metadata:%s:%s:%s:%s", version.Version, branch, runtimeVersion, updateId)
}

func ComputeUpdataManifestCacheKey(branch string, runtimeVersion string, updateId string, platform string) string {
	return fmt.Sprintf("manifest:%s:%s:%s:%s:%s", version.Version, branch, runtimeVersion, updateId, platform)
}

func ComputeManifestAssetCacheKey(update types.Update, assetPath string) string {
	return fmt.Sprintf("asset:%s:%s:%s:%s:%s", version.Version, update.Branch, update.RuntimeVersion, update.UpdateId, assetPath)
}

func VerifyUploadedUpdate(update types.Update) error {
	metadata, errMetadata := GetMetadata(update)
	if errMetadata != nil {
		return errMetadata
	}
	if metadata.MetadataJSON.FileMetadata.IOS.Bundle == "" && metadata.MetadataJSON.FileMetadata.Android.Bundle == "" {
		return fmt.Errorf("missing bundle path in metadata")
	}
	files := []string{}
	if metadata.MetadataJSON.FileMetadata.IOS.Bundle != "" {
		files = append(files, metadata.MetadataJSON.FileMetadata.IOS.Bundle)
		for _, asset := range metadata.MetadataJSON.FileMetadata.IOS.Assets {
			files = append(files, asset.Path)
		}
	}
	if metadata.MetadataJSON.FileMetadata.Android.Bundle != "" {
		files = append(files, metadata.MetadataJSON.FileMetadata.Android.Bundle)
		for _, asset := range metadata.MetadataJSON.FileMetadata.Android.Assets {
			files = append(files, asset.Path)
		}
	}

	resolvedBucket := bucket.GetBucket()
	for _, file := range files {
		f, err := resolvedBucket.GetFile(update, file)
		if err != nil {
			return fmt.Errorf("missing file: %s in update", file)
		}
		if f != nil {
			f.Reader.Close()
		}
	}
	return nil
}

func GetUpdate(branch string, runtimeVersion string, updateId string) (*types.Update, error) {
	updateIdInt64, err := strconv.ParseInt(updateId, 10, 64)
	if err != nil {
		return nil, err
	}
	return &types.Update{
		Branch:         branch,
		RuntimeVersion: runtimeVersion,
		UpdateId:       updateId,
		CreatedAt:      time.Duration(updateIdInt64) * time.Millisecond,
	}, nil
}

func AreUpdatesIdentical(update1, update2 types.Update) (bool, error) {
	metadata1, errMetadata1 := GetMetadata(update1)
	if errMetadata1 != nil {
		return false, errMetadata1
	}
	metadata2, errMetadata2 := GetMetadata(update2)
	if errMetadata2 != nil {
		return false, errMetadata2
	}
	return metadata1.Fingerprint == metadata2.Fingerprint, nil
}

func GetLatestUpdateBundlePathForRuntimeVersion(branch string, runtimeVersion string, platform string) (*types.Update, error) {
	cache := cache2.GetCache()
	cacheKey := ComputeLastUpdateCacheKey(branch, runtimeVersion, platform)
	if cachedValue := cache.Get(cacheKey); cachedValue != "" {
		var update types.Update
		err := json.Unmarshal([]byte(cachedValue), &update)
		if err != nil {
			return nil, err
		}
		return &update, nil
	}
	// List the runtime's updates (the bucket derives CreatedAt from the updateId, so
	// we can sort newest-first WITHOUT reading any per-update metadata) and scan once,
	// reading update-metadata.json + .check only until the first update that matches
	// the platform AND is valid. That update is the latest (newest-first order), so
	// the result is identical to filtering+validating every update — but a cold cache
	// miss now does O(1) bucket reads in the common case (newest update matches)
	// instead of O(N) (N=82 for an active runtime). The previous code read metadata
	// for every update (filterPlatformUpdates) AND .check for every update, so a
	// foreground herd at the 1800s lastUpdate TTL expiry stampeded the origin.
	resolvedBucket := bucket.GetBucket()
	updates, err := resolvedBucket.GetUpdates(branch, runtimeVersion)
	if err != nil {
		return nil, err
	}
	updates = sortUpdates(updates)
	for i := range updates {
		storedMetadata, metaErr := RetrieveUpdateStoredMetadata(updates[i])
		if metaErr != nil || storedMetadata == nil || storedMetadata.Platform != platform {
			continue
		}
		if !IsUpdateValid(updates[i]) {
			continue
		}
		latest := updates[i]
		cacheValue, marshalErr := json.Marshal(latest)
		if marshalErr != nil {
			return &latest, nil
		}
		ttl := 1800
		_ = cache.Set(cacheKey, string(cacheValue), &ttl)
		return &latest, nil
	}
	return nil, nil
}

func GetUpdateType(update types.Update) types.UpdateType {
	resolvedBucket := bucket.GetBucket()
	file, _ := resolvedBucket.GetFile(update, "rollback")
	if file != nil {
		file.Reader.Close()
		return types.Rollback
	}
	return types.NormalUpdate
}

func GetExpoConfig(update types.Update) (json.RawMessage, error) {
	resolvedBucket := bucket.GetBucket()
	resp, err := resolvedBucket.GetFile(update, "expoConfig.json")
	if err != nil {
		return nil, err
	}
	if resp == nil {
		// Return empty JSON if the file is not found
		return json.RawMessage("{}"), nil
	}
	defer resp.Reader.Close()
	var expoConfig json.RawMessage
	err = json.NewDecoder(resp.Reader).Decode(&expoConfig)
	if err != nil {
		return nil, err
	}
	return expoConfig, nil
}

func GetMetadata(update types.Update) (types.UpdateMetadata, error) {
	metadataCacheKey := ComputeMetadataCacheKey(update.Branch, update.RuntimeVersion, update.UpdateId)
	cache := cache2.GetCache()
	if cachedValue := cache.Get(metadataCacheKey); cachedValue != "" {
		var metadata types.UpdateMetadata
		err := json.Unmarshal([]byte(cachedValue), &metadata)
		if err != nil {
			return types.UpdateMetadata{}, err
		}
		return metadata, nil
	}
	resolvedBucket := bucket.GetBucket()
	file, errFile := resolvedBucket.GetFile(update, "metadata.json")
	if errFile != nil || file == nil {
		return types.UpdateMetadata{}, errFile
	}
	createdAt := file.CreatedAt
	var metadata types.UpdateMetadata
	var metadataJson types.MetadataObject
	err := json.NewDecoder(file.Reader).Decode(&metadataJson)
	defer file.Reader.Close()
	if err != nil {
		fmt.Println("error decoding metadata json:", err)
		return types.UpdateMetadata{}, err
	}

	metadata.CreatedAt = createdAt.UTC().Format("2006-01-02T15:04:05.000Z")
	metadata.MetadataJSON = metadataJson
	stringifiedMetadata, err := json.Marshal(metadata.MetadataJSON)
	if err != nil {
		return types.UpdateMetadata{}, err
	}
	hashInput := fmt.Sprintf("%s::%s::%s::%s", string(stringifiedMetadata), update.UpdateId, update.Branch, update.RuntimeVersion)
	id, errHash := crypto.CreateHash([]byte(hashInput), "sha256", "hex")

	if errHash != nil {
		return types.UpdateMetadata{}, errHash
	}
	fingerPrintHash := fmt.Sprintf("%s::%s::%s", string(stringifiedMetadata), update.Branch, update.RuntimeVersion)
	fingerprint, errHash := crypto.CreateHash([]byte(fingerPrintHash), "sha256", "hex")
	if errHash != nil {
		return types.UpdateMetadata{}, errHash
	}
	metadata.ID = id
	metadata.Fingerprint = fingerprint
	cacheValue, err := json.Marshal(metadata)
	if err != nil {
		return metadata, nil
	}
	err = cache.Set(metadataCacheKey, string(cacheValue), nil)
	return metadata, nil
}

func BuildFinalManifestAssetUrlURL(baseURL, assetFilePath, runtimeVersion, platform, branch string) (string, error) {
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}
	query := url.Values{}
	query.Set("asset", assetFilePath)
	query.Set("runtimeVersion", runtimeVersion)
	query.Set("platform", platform)
	query.Set("branch", branch)
	// Also set random query parameter to prevent caching issues
	parsedURL.RawQuery = query.Encode()
	return parsedURL.String(), nil
}

func GetAssetEndpoint() string {
	return config.GetEnv("BASE_URL") + "/assets"
}

func shapeManifestAsset(update types.Update, asset *types.Asset, isLaunchAsset bool, platform string) (types.ManifestAsset, error) {
	cacheKey := ComputeManifestAssetCacheKey(update, asset.Path)
	cache := cache2.GetCache()
	if cachedValue := cache.Get(cacheKey); cachedValue != "" {
		var manifestAsset types.ManifestAsset
		err := json.Unmarshal([]byte(cachedValue), &manifestAsset)
		if err != nil {
			return types.ManifestAsset{}, err
		}
		return manifestAsset, nil
	}
	resolvedBucket := bucket.GetBucket()
	assetFilePath := asset.Path
	assetFile, errAssetFile := resolvedBucket.GetFile(update, asset.Path)
	if errAssetFile != nil {
		return types.ManifestAsset{}, errAssetFile
	}
	if assetFile == nil {
		return types.ManifestAsset{}, fmt.Errorf("asset file not found: %s", asset.Path)
	}

	byteAsset, errAsset := bucket.ConvertReadCloserToBytes(assetFile.Reader)
	defer assetFile.Reader.Close()
	if errAsset != nil {
		return types.ManifestAsset{}, errAsset
	}
	assetHash, errHash := crypto.CreateHash(byteAsset, "sha256", "base64")
	if errHash != nil {
		return types.ManifestAsset{}, errHash
	}
	urlEncodedHash := crypto.GetBase64URLEncoding(assetHash)
	key, errKey := crypto.CreateHash(byteAsset, "md5", "hex")
	if errKey != nil {
		return types.ManifestAsset{}, errKey
	}

	keyExtensionSuffix := asset.Ext
	if isLaunchAsset {
		keyExtensionSuffix = "bundle"
	}
	keyExtensionSuffix = "." + keyExtensionSuffix
	contentType := "application/javascript"
	if isLaunchAsset {
		contentType = mime.TypeByExtension(asset.Ext)
	}
	finalUrl, errUrl := BuildFinalManifestAssetUrlURL(GetAssetEndpoint(), assetFilePath, update.RuntimeVersion, platform, update.Branch)
	if errUrl != nil {
		return types.ManifestAsset{}, errUrl
	}
	manifestAsset := types.ManifestAsset{
		Hash:          urlEncodedHash,
		Key:           key,
		FileExtension: keyExtensionSuffix,
		ContentType:   contentType,
		Url:           finalUrl,
	}
	cacheValue, err := json.Marshal(manifestAsset)
	if err != nil {
		return manifestAsset, nil
	}
	_ = cache.Set(cacheKey, string(cacheValue), nil)
	return manifestAsset, nil
}

func appendChannelOverrideToUrl(urlStr string) string {
	parsedUrl, err := url.Parse(urlStr)
	if err != nil {
		return urlStr
	}
	query := parsedUrl.Query()
	parsedUrl.RawQuery = query.Encode()
	return parsedUrl.String()
}

func computeManifestMetadata(update types.Update) json.RawMessage {
	metadataMap := map[string]string{
		"branch": update.Branch,
	}

	metadataBytes, err := json.Marshal(metadataMap)
	if err != nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(metadataBytes)
}

func ComposeUpdateManifest(
	metadata *types.UpdateMetadata,
	update types.Update,
	platform string,
) (types.UpdateManifest, error) {
	cache := cache2.GetCache()
	cacheKey := ComputeUpdataManifestCacheKey(update.Branch, update.RuntimeVersion, update.UpdateId, platform)
	if cachedValue := cache.Get(cacheKey); cachedValue != "" {
		var manifest types.UpdateManifest
		err := json.Unmarshal([]byte(cachedValue), &manifest)
		if err != nil {
			return types.UpdateManifest{}, err
		}
		return manifest, nil
	}
	expoConfig, errConfig := GetExpoConfig(update)
	if errConfig != nil {
		return types.UpdateManifest{}, errConfig
	}
	storedMetadata, _ := RetrieveUpdateStoredMetadata(update)
	if storedMetadata == nil || storedMetadata.UpdateUUID == "" {
		storedMetadata = &types.UpdateStoredMetadata{
			Platform:   platform,
			CommitHash: "",
			UpdateUUID: crypto.ConvertSHA256HashToUUID(metadata.ID),
		}
	}

	var platformSpecificMetadata types.PlatformMetadata
	switch platform {
	case "ios":
		platformSpecificMetadata = metadata.MetadataJSON.FileMetadata.IOS
	case "android":
		platformSpecificMetadata = metadata.MetadataJSON.FileMetadata.Android
	}
	if platformSpecificMetadata.Bundle == "" {
		return types.UpdateManifest{}, fmt.Errorf("platform %s not supported", platform)
	}
	var (
		assets = make([]types.ManifestAsset, len(platformSpecificMetadata.Assets))
		errs   = make(chan error, len(platformSpecificMetadata.Assets))
		wg     sync.WaitGroup
	)

	for i, a := range platformSpecificMetadata.Assets {
		wg.Add(1)
		go func(index int, asset types.Asset) {
			defer wg.Done()
			shapedAsset, errShape := shapeManifestAsset(update, &asset, false, platform)
			if errShape != nil {
				errs <- errShape
				return
			}
			assets[index] = shapedAsset
		}(i, a)
	}

	wg.Wait()
	close(errs)

	if len(errs) > 0 {
		return types.UpdateManifest{}, <-errs
	}

	launchAsset, errShape := shapeManifestAsset(update, &types.Asset{
		Path: platformSpecificMetadata.Bundle,
		Ext:  "",
	}, true, platform)
	if errShape != nil {
		return types.UpdateManifest{}, errShape
	}

	manifest := types.UpdateManifest{
		Id:             storedMetadata.UpdateUUID,
		CreatedAt:      metadata.CreatedAt,
		RunTimeVersion: update.RuntimeVersion,
		Metadata:       computeManifestMetadata(update),
		Extra: types.ExtraManifestData{
			ExpoClient: expoConfig,
			Branch:     update.Branch,
		},
		Assets:      assets,
		LaunchAsset: launchAsset,
	}
	cacheValue, err := json.Marshal(manifest)
	if err != nil {
		return manifest, nil
	}
	_ = cache.Set(cacheKey, string(cacheValue), nil)

	return manifest, nil
}

func CreateRollbackDirective(update types.Update) (types.RollbackDirective, error) {
	resolvedBucket := bucket.GetBucket()
	object, err := resolvedBucket.GetFile(update, "rollback")
	if err != nil {
		return types.RollbackDirective{}, err
	}
	commitTime := object.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	defer object.Reader.Close()
	return types.RollbackDirective{
		Type: "rollBackToEmbedded",
		Parameters: types.RollbackDirectiveParameters{
			CommitTime: commitTime,
		},
	}, nil
}

func CreateNoUpdateAvailableDirective() types.NoUpdateAvailableDirective {
	return types.NoUpdateAvailableDirective{
		Type: "noUpdateAvailable",
	}
}

func RetrieveUpdateStoredMetadata(update types.Update) (*types.UpdateStoredMetadata, error) {
	resolvedBucket := bucket.GetBucket()
	file, err := resolvedBucket.GetFile(update, "update-metadata.json")
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, nil
	}
	defer file.Reader.Close()
	var metadata types.UpdateStoredMetadata
	err = json.NewDecoder(file.Reader).Decode(&metadata)
	if err != nil {
		return nil, err
	}
	return &metadata, nil
}

func createUpdateMetadata(platform, commitHash string) (*strings.Reader, error) {
	metadata := map[string]string{
		"platform":   platform,
		"commitHash": commitHash,
	}

	jsonData, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}

	return strings.NewReader(string(jsonData)), nil
}

func GenerateUpdateTimestamp() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}

func ConvertUpdateTimestampToString(updateId int64) string {
	return fmt.Sprintf("%d", updateId)
}

func CreateRollback(platform, commitHash, runtimeVersion, branchName string) (*types.Update, error) {
	updateId := GenerateUpdateTimestamp()
	update := types.Update{
		UpdateId:       ConvertUpdateTimestampToString(updateId),
		Branch:         branchName,
		RuntimeVersion: runtimeVersion,
		CreatedAt:      time.Duration(updateId) * time.Millisecond,
	}
	resolvedBucket := bucket.GetBucket()
	reader, err := createUpdateMetadata(platform, commitHash)
	if err != nil {
		return nil, err
	}
	err = resolvedBucket.UploadFileIntoUpdate(update, "update-metadata.json", reader)
	if err != nil {
		return nil, err
	}
	emptyReader := strings.NewReader("")
	err = resolvedBucket.UploadFileIntoUpdate(update, "rollback", emptyReader)
	if err != nil {
		return nil, err
	}
	err = StoreUpdateUUIDInMetadata(update)
	if err != nil {
		return nil, err
	}
	err = MarkUpdateAsChecked(update)
	if err != nil {
		return nil, err
	}

	return &update, nil
}

func RepublishUpdate(previousUpdate *types.Update, platform, commitHash string) (*types.Update, error) {
	resolvedBucket := bucket.GetBucket()
	updateId := GenerateUpdateTimestamp()
	newUpdate, err := resolvedBucket.CreateUpdateFrom(previousUpdate, ConvertUpdateTimestampToString(updateId))
	if err != nil {
		return nil, err
	}
	reader, err := createUpdateMetadata(platform, commitHash)
	if err != nil {
		return nil, err
	}
	err = resolvedBucket.UploadFileIntoUpdate(*newUpdate, "update-metadata.json", reader)
	if err != nil {
		return nil, err
	}
	err = StoreUpdateUUIDInMetadata(*newUpdate)
	if err != nil {
		return nil, err
	}
	err = MarkUpdateAsChecked(*newUpdate)
	if err != nil {
		return nil, err
	}
	return newUpdate, nil
}
