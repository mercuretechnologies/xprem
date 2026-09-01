package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"xprem/config"
	"xprem/internal/version"
)

func IsDashboardEnabled() bool {
	return config.GetEnv("USE_DASHBOARD") == "true"
}

// The app payloads carry the repository URL, which comes from an env var, not
// the store: without it in the key, a cache outliving the process (redis) would
// keep serving the old value past a restart.
func repositoryFingerprint() string {
	sum := sha256.Sum256([]byte(config.RepositoryURL()))
	return hex.EncodeToString(sum[:4])
}

// Dashboard cache keys must include the appId so entries from one app aren't
// served to another within the TTL (multi-tenant cache bleeding).

func ComputeGetAppCacheKey(appId string) string {
	return fmt.Sprintf("dashboard:%s:%s:app:%s:request:getApp", version.Version, repositoryFingerprint(), appId)
}

func ComputeGetAppsCacheKey() string {
	return fmt.Sprintf("dashboard:%s:%s:request:getApps", version.Version, repositoryFingerprint())
}

func ComputeGetRuntimeVersionsCacheKey(appId, branch string) string {
	return fmt.Sprintf("dashboard:%s:%s:request:getRuntimeVersions:%s", version.Version, appId, branch)
}

func ComputeGetBranchesCacheKey(appId string) string {
	return fmt.Sprintf("dashboard:%s:%s:request:getBranches", version.Version, appId)
}

func ComputeGetChannelsCacheKey(appId string) string {
	return fmt.Sprintf("dashboard:%s:%s:request:getChannels", version.Version, appId)
}

func ComputeGetUpdateDetailsCacheKey(appId, branch, runtimeVersion, updateID string) string {
	return fmt.Sprintf("dashboard:%s:%s:request:getUpdateDetails:%s:%s:%s", version.Version, appId, branch, runtimeVersion, updateID)
}

func ComputeGetApiKeysCacheKey(appId string) string {
	return fmt.Sprintf("dashboard:%s:%s:request:getApiKeys", version.Version, appId)
}
