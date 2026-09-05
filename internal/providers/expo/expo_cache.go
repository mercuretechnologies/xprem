package expo

import (
	"crypto/sha256"
	"fmt"
	cache2 "xprem/internal/cache"
	"xprem/internal/types"
	"xprem/internal/version"
)

// The expo-provider cache namespace: Expo API lookups only this package
// reads. Tokens and session secrets are hashed into the keys, never stored.
const (
	userAccountCacheTTLSeconds      = 300
	selfUsernameCacheTTLSeconds     = 86400
	appNameCacheTTLSeconds          = 86400
	unknownAppNameCacheTTLSeconds   = 300
	channelMappingCacheTTLSeconds   = 60
	accountAppsCacheTTLSeconds      = 300
	projectStructureCacheTTLSeconds = 60
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

// credentialFingerprint hashes whichever credential the auth carries; ""
// when it carries none, which disables caching for the call.
func credentialFingerprint(auth types.Auth) string {
	if auth.Token != nil && *auth.Token != "" {
		return fmt.Sprintf("token:%x", sha256.Sum256([]byte(*auth.Token)))
	}
	if auth.SessionSecret != nil && *auth.SessionSecret != "" {
		return fmt.Sprintf("session:%x", sha256.Sum256([]byte(*auth.SessionSecret)))
	}
	return ""
}

func accountAppsCacheKey(fingerprint string) string {
	return fmt.Sprintf("expo-provider:account-apps:%s:%s", version.Version, fingerprint)
}

func projectStructureCacheKey(appId string, fingerprint string) string {
	return fmt.Sprintf("expo-provider:project-structure:%s:%s:%s", version.Version, appId, fingerprint)
}

// InvalidateAccountApps drops the cached account listing for this credential.
func InvalidateAccountApps(auth types.Auth) {
	if fingerprint := credentialFingerprint(auth); fingerprint != "" {
		cache2.GetCache().Delete(accountAppsCacheKey(fingerprint))
	}
}

// InvalidateProjectStructure drops the cached project structure for this
// credential and project.
func InvalidateProjectStructure(auth types.Auth, expoAppId string) {
	if fingerprint := credentialFingerprint(auth); fingerprint != "" {
		cache2.GetCache().Delete(projectStructureCacheKey(expoAppId, fingerprint))
	}
}
