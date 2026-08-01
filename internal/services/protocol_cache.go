package services

import (
	"context"
	"fmt"
	"strconv"
	"xprem/config"
	cache2 "xprem/internal/cache"
	"xprem/internal/providers/expo"
	"xprem/internal/types"
	"xprem/internal/version"
)

// The read-through caches of the update-delivery hot path: in steady state a
// manifest or asset poll is answered without a single repository read.
//
// The 10s TTLs are the freshness bound for operator changes, there is no
// invalidation beyond the dashboard's channel-mapping deletes. Signatures can
// live longer: their key embeds the signing key fingerprint and the content
// hash, so a stale entry can never be served.
const (
	signatureCacheTTLSeconds      = 3600
	appConfigCacheTTLSeconds      = 10
	channelMappingCacheTTLSeconds = 10
	updateTypeCacheTTLSeconds     = 10
)

func appConfigCacheKey(appId string) string {
	return fmt.Sprintf("app-config:%s:%s", version.Version, appId)
}

func updateTypeCacheKey(update types.Update) string {
	return fmt.Sprintf("update-type:%s:%s:%s:%s:%s", version.Version, update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId)
}

func signatureCacheKey(appId string, keyFingerprint string, contentHash string) string {
	return fmt.Sprintf("manifest-signature:%s:%s:%s:%s", version.Version, appId, keyFingerprint, contentHash)
}

func (s *ExpoProtocolService) cachedAppConfig(ctx context.Context, appId string) (config.AppConfig, error) {
	appCache := cache2.GetCache()
	if appConfig, ok := cache2.GetJSON[config.AppConfig](appCache, appConfigCacheKey(appId)); ok {
		return appConfig, nil
	}
	appConfig, err := s.appRepo.GetAppByID(ctx, appId)
	if err != nil {
		return config.AppConfig{}, err
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

// channelBranchMapping reads the mapping through the same cache key the
// dashboard invalidates on channel and rollout writes. An unknown or unmapped
// channel (nil) is never cached.
func (s *ExpoProtocolService) channelBranchMapping(ctx context.Context, appId string, channelName string) (*expo.ChannelMapping, error) {
	mappingCache := cache2.GetCache()
	cacheKey := expo.ComputeChannelMappingCacheKey(appId, channelName)
	if mapping, ok := cache2.GetJSON[expo.ChannelMapping](mappingCache, cacheKey); ok {
		return &mapping, nil
	}
	mapping, err := s.channelRepo.GetChannelBranchMapping(ctx, appId, channelName)
	if err != nil || mapping == nil {
		return mapping, err
	}
	ttl := channelMappingCacheTTLSeconds
	cache2.SetJSON(mappingCache, cacheKey, mapping, &ttl)
	return mapping, nil
}
