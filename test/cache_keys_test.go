package test

import (
	"strings"
	"testing"
	"xprem/internal/dashboard"
	"xprem/internal/types"
	"xprem/internal/update"
	"xprem/internal/version"
)

// Serialized-payload cache keys must embed the release version: entries
// surviving an upgrade in Redis must never be decoded by a binary whose
// payload shapes changed. Private builders have the same guard in their own
// packages (internal/services, internal/providers/expo).
func TestExportedPayloadCacheKeysEmbedReleaseVersion(t *testing.T) {
	u := types.Update{AppId: "app", Branch: "branch", RuntimeVersion: "rt", UpdateId: "1"}
	keys := map[string]string{
		"lastUpdate":       update.ComputeLastUpdateCacheKey("app", "branch", "rt", "ios"),
		"metadata":         update.ComputeMetadataCacheKey("app", "branch", "rt", "1"),
		"manifestResponse": update.ComputeManifestResponseCacheKey("app", "branch", "rt", "1", "ios"),
		"manifestAsset":    update.ComputeManifestAssetCacheKey("app", u, "bundle.js"),
		"getApp":           dashboard.ComputeGetAppCacheKey("app"),
		"getApps":          dashboard.ComputeGetAppsCacheKey(),
		"getRuntimes":      dashboard.ComputeGetRuntimeVersionsCacheKey("app", "branch"),
		"getBranches":      dashboard.ComputeGetBranchesCacheKey("app"),
		"getChannels":      dashboard.ComputeGetChannelsCacheKey("app"),
		"getUpdateDetails": dashboard.ComputeGetUpdateDetailsCacheKey("app", "branch", "rt", "1"),
		"getApiKeys":       dashboard.ComputeGetApiKeysCacheKey("app"),
	}
	for name, key := range keys {
		if !strings.Contains(key, version.Version) {
			t.Errorf("%s cache key %q does not embed version.Version", name, key)
		}
	}
}
