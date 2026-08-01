package expo

import (
	"crypto/sha256"
	"fmt"
	cache2 "xprem/internal/cache"
	"xprem/internal/version"
)

// The expo-provider cache namespace: Expo API lookups only this package
// reads. Tokens and session secrets are hashed into the keys, never stored.
const (
	userAccountCacheTTLSeconds    = 300
	selfUsernameCacheTTLSeconds   = 86400
	appNameCacheTTLSeconds        = 86400
	unknownAppNameCacheTTLSeconds = 300
	channelMappingCacheTTLSeconds = 60
)

func userAccountTokenCacheKey(token string) string {
	return fmt.Sprintf("expo-provider:user-account:%s:token:%x", version.Version, sha256.Sum256([]byte(token)))
}

func userAccountSessionCacheKey(sessionSecret string) string {
	return fmt.Sprintf("expo-provider:user-account:%s:session:%x", version.Version, sha256.Sum256([]byte(sessionSecret)))
}

func selfUsernameCacheKey(token string) string {
	return fmt.Sprintf("expo-provider:self-username:%s:%x", version.Version, sha256.Sum256([]byte(token)))
}

func appNameCacheKey(appId string, token string) string {
	return fmt.Sprintf("expo-provider:app-name:%s:%s:%x", version.Version, appId, sha256.Sum256([]byte(token)))
}

func channelMappingCacheKey(appId, channelName string) string {
	return fmt.Sprintf("expo-provider:channel-mapping:%s:%s:%s", version.Version, appId, channelName)
}

// InvalidateChannelMapping drops this provider's cached mapping; callers ask
// for the behavior, the key format stays private.
func InvalidateChannelMapping(appId, channelName string) {
	cache2.GetCache().Delete(channelMappingCacheKey(appId, channelName))
}
