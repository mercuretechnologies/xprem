package services

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"xprem/config"
	cache2 "xprem/internal/cache"
	"xprem/internal/types"
	"xprem/internal/version"
)

// The read-through caches of the update-delivery hot path: in steady state a
// manifest or asset poll is answered without a single repository read.
//
// The short TTLs below are the freshness bound for operator changes: nothing
// invalidates these keys. Signatures can live longer: their key embeds the
// signing key fingerprint and the content hash, so a stale entry can never be
// served.
const (
	signatureCacheTTLSeconds = 3600
	appConfigCacheTTLSeconds = 10
	// 5s keeps a channel remap or rollout promote near-instant for devices.
	channelMappingCacheTTLSeconds = 5
	updateTypeCacheTTLSeconds     = 10
	// Unlike its neighbours this key IS invalidated on write, but only where
	// the cache is shared: with a local cache and several replicas, the write
	// clears one process and the TTL bounds the rest.
	channelBranchSurfingCacheTTLSeconds = 30
	// Every launch of every build carrying the picker reads the branch list, so
	// this one absorbs a fleet-wide boot rather than operator edits. Short enough
	// that a publish shows up the next time a tester opens the panel.
	surfableBranchesCacheTTLSeconds = 15
)

func appConfigCacheKey(appId string) string {
	return fmt.Sprintf("app-config:%s:%s", version.Version, appId)
}

func updateTypeCacheKey(update types.Update) string {
	return fmt.Sprintf("update-type:%s:%s:%s:%s:%s", version.Version, update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId)
}

func channelMappingCacheKey(appId string, channelName string) string {
	return fmt.Sprintf("channel-mapping:%s:%s:%s", version.Version, appId, channelName)
}

func channelBranchSurfingCacheKey(appId string, channelName string) string {
	return fmt.Sprintf("channel-branch-surfing:%s:%s:%s", version.Version, appId, channelName)
}

// Keyed without the channel: the list is per app, runtime version and platform,
// and the channel's pattern filters it afterwards, so channels share one entry.
// The platform belongs in the key, not just the query — an iOS answer served to
// an Android device would offer branches it cannot be given.
func surfableBranchesCacheKey(appId string, runtimeVersion string, platform string) string {
	return fmt.Sprintf("surfable-branches:%s:%s:%s:%s", version.Version, appId, runtimeVersion, platform)
}

func cachedSurfableBranches(ctx context.Context, branchRepo BranchRepository, appId string, runtimeVersion string, platform string) ([]types.SurfableBranch, error) {
	branchCache := cache2.GetCache()
	cacheKey := surfableBranchesCacheKey(appId, runtimeVersion, platform)
	if branches, ok := cache2.GetJSON[[]types.SurfableBranch](branchCache, cacheKey); ok {
		return branches, nil
	}
	branches, err := branchRepo.GetSurfableBranches(ctx, appId, runtimeVersion, platform)
	if err != nil {
		return nil, err
	}
	ttl := surfableBranchesCacheTTLSeconds
	// An empty answer is cached too: a runtime version with nothing built for it
	// is exactly the request an attacker repeats.
	cache2.SetJSON(branchCache, cacheKey, branches, &ttl)
	return branches, nil
}

func signatureCacheKey(appId string, keyFingerprint string, contentHash string) string {
	return fmt.Sprintf("manifest-signature:%s:%s:%s:%s", version.Version, appId, keyFingerprint, contentHash)
}

// carriesPlaintextSecret reports whether an app config holds material that
// must never reach a shared cache: the Expo access token, or a signing key
// passed in the clear through the environment. Sealed keys are encrypted with
// the master key and secret ids and paths are references, not secrets.
func carriesPlaintextSecret(appConfig config.AppConfig) bool {
	return appConfig.AccessToken != "" || appConfig.Keys.PrivateB64 != ""
}

// cachedAppConfig is the hot-path read of an app: manifest and asset requests
// go through it on every poll. A config carrying a plaintext secret is served
// but never stored, which costs nothing: those configs come from the flat env,
// where the underlying read is already an in-memory map lookup.
func (s *ExpoProtocolService) cachedAppConfig(ctx context.Context, appId string) (config.AppConfig, error) {
	appCache := cache2.GetCache()
	if appConfig, ok := cache2.GetJSON[config.AppConfig](appCache, appConfigCacheKey(appId)); ok {
		return appConfig, nil
	}
	appConfig, err := s.appRepo.GetAppByID(ctx, appId)
	if err != nil {
		return config.AppConfig{}, err
	}
	if carriesPlaintextSecret(appConfig) {
		return appConfig, nil
	}
	ttl := appConfigCacheTTLSeconds
	cache2.SetJSON(appCache, appConfigCacheKey(appId), appConfig, &ttl)
	return appConfig, nil
}

func (s *ExpoProtocolService) cachedUpdateType(ctx context.Context, update types.Update) (types.UpdateType, error) {
	typeCache := cache2.GetCache()
	cacheKey := updateTypeCacheKey(update)
	if cached := typeCache.Get(cacheKey); cached != "" {
		if parsed, err := strconv.Atoi(cached); err == nil {
			return types.UpdateType(parsed), nil
		}
	}
	updateType, err := s.updateRepo.GetUpdateType(ctx, update)
	if err != nil {
		return 0, err
	}
	ttl := updateTypeCacheTTLSeconds
	_ = typeCache.Set(cacheKey, strconv.Itoa(int(updateType)), &ttl)
	return updateType, nil
}

// channelBranchMapping owns the delivery path's mapping cache: no cross-layer
// invalidation, the TTL is the freshness bound for channel and rollout edits.
// In stateless mode it stacks on the expo provider's own cache, so an edit
// made outside the dashboard can take the provider's TTL plus this one to
// reach devices.
// An unknown or unmapped channel (nil) is never cached.
func (s *ExpoProtocolService) channelBranchMapping(ctx context.Context, appId string, channelName string) (*types.ChannelResolution, error) {
	return cachedChannelMapping(ctx, s.channelRepo, appId, channelName)
}

// cachedChannelMapping is channelBranchMapping without the service, so the
// branch list can read the same entry the delivery path already fills.
func cachedChannelMapping(ctx context.Context, channelRepo ChannelRepository, appId string, channelName string) (*types.ChannelResolution, error) {
	mappingCache := cache2.GetCache()
	cacheKey := channelMappingCacheKey(appId, channelName)
	if mapping, ok := cache2.GetJSON[types.ChannelResolution](mappingCache, cacheKey); ok {
		return &mapping, nil
	}
	mapping, err := channelRepo.GetChannelBranchMapping(ctx, appId, channelName)
	if err != nil || mapping == nil {
		return mapping, err
	}
	ttl := channelMappingCacheTTLSeconds
	cache2.SetJSON(mappingCache, cacheKey, mapping, &ttl)
	return mapping, nil
}

// branchSurfingEnabled answers whether the channel lets devices ask for another
// branch. It is on the manifest hot path, so it never fails the poll: any error,
// and stateless mode where the setting does not exist, answer false.
func (s *ExpoProtocolService) branchSurfingEnabled(ctx context.Context, appId string, channelName string) (bool, string) {
	return cachedBranchSurfing(ctx, s.channelRepo, appId, channelName)
}

func cachedBranchSurfing(ctx context.Context, channelRepo ChannelRepository, appId string, channelName string) (bool, string) {
	if !config.IsDBMode() || channelName == "" {
		return false, ""
	}
	surfingCache := cache2.GetCache()
	cacheKey := channelBranchSurfingCacheKey(appId, channelName)
	// "<0|1>:<pattern>". A value without the separator predates this format, so
	// it falls through to the read below, which rewrites it.
	if cached := surfingCache.Get(cacheKey); cached != "" {
		if enabled, pattern, ok := strings.Cut(cached, ":"); ok {
			return enabled == "1", pattern
		}
	}
	surfing, err := channelRepo.GetBranchSurfing(ctx, appId, channelName)
	if err != nil {
		// Deliberately not cached: an error is not an answer, and caching one
		// would keep a channel dark for the whole TTL after the database
		// recovers. Costs a read per poll only while the database is down.
		return false, ""
	}
	ttl := channelBranchSurfingCacheTTLSeconds
	if surfing == nil {
		// A channel that does not exist is cached exactly like one with surfing
		// off. Leaving it uncached made the two observably different: the
		// disabled channel answered from memory and the unknown one hit
		// Postgres, so response time told an unauthenticated caller which
		// channel names exist — and let it drive an unbounded read per request.
		_ = surfingCache.Set(cacheKey, boolCacheValue(false)+":", &ttl)
		return false, ""
	}
	_ = surfingCache.Set(cacheKey, boolCacheValue(surfing.Enabled)+":"+surfing.Pattern, &ttl)
	return surfing.Enabled, surfing.Pattern
}

// invalidateBranchSurfingCache drops the delivery-path entry a setting write
// stales. Best effort: see channelBranchSurfingCacheTTLSeconds.
func invalidateBranchSurfingCache(appId string, channelName string) {
	cache2.GetCache().Delete(channelBranchSurfingCacheKey(appId, channelName))
}

func ForgetBranchSurfing(appId string, channelName string) {
	invalidateBranchSurfingCache(appId, channelName)
}

func ForgetSurfableBranches(appId string, runtimeVersion string, platform string) {
	cache2.GetCache().Delete(surfableBranchesCacheKey(appId, runtimeVersion, platform))
}

func boolCacheValue(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
