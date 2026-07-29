package dashboard

import (
	"fmt"
	"xprem/config"
	"xprem/internal/version"
)

func IsDashboardEnabled() bool {
	return config.GetEnv("USE_DASHBOARD") == "true"
}

// Dashboard cache keys must include the appId so entries from one app aren't
// served to another within the TTL (multi-tenant cache bleeding).

func ComputeGetAppCacheKey(appId string) string {
	return fmt.Sprintf("dashboard:%s:app:%s:request:getApp", version.Version, appId)
}

func ComputeGetAppsCacheKey() string {
	return fmt.Sprintf("dashboard:%s:request:getApps", version.Version)
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

func ComputeGetUpdatesCacheKey(appId, branch, runtimeVersion string) string {
	return fmt.Sprintf("dashboard:%s:%s:request:getUpdates:%s:%s", version.Version, appId, branch, runtimeVersion)
}

func ComputeGetUpdateDetailsCacheKey(appId, branch, runtimeVersion, updateID string) string {
	return fmt.Sprintf("dashboard:%s:%s:request:getUpdateDetails:%s:%s:%s", version.Version, appId, branch, runtimeVersion, updateID)
}

func ComputeGetApiKeysCacheKey(appId string) string {
	return fmt.Sprintf("dashboard:%s:%s:request:getApiKeys", version.Version, appId)
}
