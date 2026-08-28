package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"
	"xprem/config"
	"xprem/internal/bucket"
	cache2 "xprem/internal/cache"
	"xprem/internal/crypto"
	"xprem/internal/types"
	"xprem/internal/version"
)

var ErrUpdateMetadataMissing = errors.New("metadata.json missing from storage")

func GetUpdateCheckStatus(update types.Update) time.Time {
	resolvedBucket := bucket.GetBucket()
	file, err := resolvedBucket.GetFile(update, ".check")
	if err != nil {
		return time.Time{}
	}
	if file == nil {
		return time.Time{}
	}
	defer file.Reader.Close()
	return file.CreatedAt.UTC()
}

func ComputeLastUpdateCacheKey(appId string, branch string, runtimeVersion string, platform types.Platform) string {
	return fmt.Sprintf("lastUpdate:%s:%s:%s:%s:%s", appId, version.Version, branch, runtimeVersion, platform)
}

func ComputeMetadataCacheKey(appId string, branch string, runtimeVersion string, updateId string) string {
	return fmt.Sprintf("metadata:%s:%s:%s:%s:%s", appId, version.Version, branch, runtimeVersion, updateId)
}

func ComputeUpdateManifestCacheKey(appId string, branch string, runtimeVersion string, updateId string, platform types.Platform) string {
	return fmt.Sprintf("manifest:%s:%s:%s:%s:%s:%s", appId, version.Version, branch, runtimeVersion, updateId, platform)
}

func ComputeManifestAssetCacheKey(appId string, update types.Update, assetPath string) string {
	return fmt.Sprintf("asset:%s:%s:%s:%s:%s:%s", appId, version.Version, update.Branch, update.RuntimeVersion, update.UpdateId, assetPath)
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

func AreUpdatesIdentical(update1, update2 types.Update) (bool, error) {
	// An update without metadata (a rollback folder, a partial upload) can
	// never be identical to another update; comparing must not fail on it.
	metadata1, errMetadata1 := GetMetadata(update1)
	if errors.Is(errMetadata1, ErrUpdateMetadataMissing) {
		return false, nil
	}
	if errMetadata1 != nil {
		return false, errMetadata1
	}
	metadata2, errMetadata2 := GetMetadata(update2)
	if errors.Is(errMetadata2, ErrUpdateMetadataMissing) {
		return false, nil
	}
	if errMetadata2 != nil {
		return false, errMetadata2
	}
	return metadata1.Fingerprint == metadata2.Fingerprint, nil
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
	metadataCacheKey := ComputeMetadataCacheKey(update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId)
	metadataCache := cache2.GetCache()
	if metadata, ok := cache2.GetJSON[types.UpdateMetadata](metadataCache, metadataCacheKey); ok {
		return metadata, nil
	}
	resolvedBucket := bucket.GetBucket()
	file, errFile := resolvedBucket.GetFile(update, "metadata.json")
	if errFile != nil {
		return types.UpdateMetadata{}, errFile
	}
	if file == nil {
		return types.UpdateMetadata{}, fmt.Errorf("%w for update %s/%s/%s/%s", ErrUpdateMetadataMissing, update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId)
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
	cache2.SetJSON(metadataCache, metadataCacheKey, metadata, nil)
	return metadata, nil
}

func BuildFinalManifestAssetUrlURL(baseURL, assetFilePath, runtimeVersion string, platform types.Platform, branch, updateId string) (string, error) {
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}
	query := url.Values{}
	query.Set("asset", assetFilePath)
	query.Set("runtimeVersion", runtimeVersion)
	query.Set("platform", string(platform))
	query.Set("branch", branch)
	// Pins the asset to the exact update the manifest came from, so rollout
	// clients on a non-latest update fetch from the right folder (control-plane
	// asset resolution validates and honors it; stateless mode ignores it).
	query.Set("updateId", updateId)
	parsedURL.RawQuery = query.Encode()
	return parsedURL.String(), nil
}

func GetAssetEndpoint() string {
	return config.BaseURL() + "/assets"
}

func shapeManifestAsset(update types.Update, asset *types.Asset, isLaunchAsset bool, platform types.Platform) (types.ManifestAsset, error) {
	cacheKey := ComputeManifestAssetCacheKey(update.AppId, update, asset.Path)
	assetCache := cache2.GetCache()
	if manifestAsset, ok := cache2.GetJSON[types.ManifestAsset](assetCache, cacheKey); ok {
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
	contentType := AssetContentType(asset.Ext, isLaunchAsset)
	finalUrl, errUrl := BuildFinalManifestAssetUrlURL(GetAssetEndpoint(), assetFilePath, update.RuntimeVersion, platform, update.Branch, update.UpdateId)
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
	cache2.SetJSON(assetCache, cacheKey, manifestAsset, nil)
	return manifestAsset, nil
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
	platform types.Platform,
) (types.UpdateManifest, error) {
	manifestCache := cache2.GetCache()
	cacheKey := ComputeUpdateManifestCacheKey(update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId, platform)
	if manifest, ok := cache2.GetJSON[types.UpdateManifest](manifestCache, cacheKey); ok {
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

	platformSpecificMetadata, err := metadata.MetadataJSON.FileMetadata.PlatformMetadata(platform)
	if err != nil || platformSpecificMetadata.Bundle == "" {
		return types.UpdateManifest{}, fmt.Errorf("platform %s not supported by update %s/%s/%s/%s", platform, update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId)
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
	cache2.SetJSON(manifestCache, cacheKey, manifest, nil)

	return manifest, nil
}

func CreateRollbackDirective(update types.Update, commitTime string) (types.RollbackDirective, error) {
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

// Originally, this function returned a raw millisecond timestamp without parameters.
// When deployment clients (like the Expo CLI) send concurrent, parallel requests for
// both iOS and Android simultaneously, they arrive at the server in the same millisecond.
//
//   - In No-DB (Stateless) Mode: Outbound HTTP network hops to external Expo verification
//     APIs introduce variable internet delays. This naturally staggered the execution
//     threads across distinct millisecond clock ticks, hiding duplicate-ID risks.
//
//   - In DB Mode: Because operations complete in microseconds, both the iOS and Android
//     execution threads regularly reach this generation line within the exact same 1ms window.
//
// To prevent concurrent platform requests from generating identical IDs and triggering
// unique-key/constraint conflicts in relational stores, we append a deterministic
// platform identifier digit (+1 for iOS, +2 for Android) to the end of the timestamp,
// mathematically decoupling their identities under any hardware concurrency schedule.
func GenerateUpdateTimestamp(platform types.Platform) int64 {
	milli := time.Now().UnixNano() / int64(time.Millisecond)
	var platformModifier int64 = 0
	switch platform {
	case types.PlatformIOS:
		platformModifier = 1
	case types.PlatformAndroid:
		platformModifier = 2
	default:
		platformModifier = 9
	}
	return milli*10 + platformModifier
}

func ConvertUpdateTimestampToString(updateId int64) string {
	return fmt.Sprintf("%d", updateId)
}
