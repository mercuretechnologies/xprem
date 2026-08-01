package services

import (
	"strings"
	"testing"
	"xprem/internal/types"
	"xprem/internal/version"
)

// Serialized-payload cache keys must embed the release version: entries
// surviving an upgrade in Redis must never be decoded by a binary whose
// payload shapes changed. Coordination keys (locks, counters, sets) must NOT
// embed it; withPrefix deliberately adds no version for them.
func TestPayloadCacheKeysEmbedReleaseVersion(t *testing.T) {
	update := types.Update{AppId: "app", Branch: "branch", RuntimeVersion: "rt", UpdateId: "1"}
	keys := map[string]string{
		"appConfig":      appConfigCacheKey("app"),
		"updateType":     updateTypeCacheKey(update),
		"channelMapping": channelMappingCacheKey("app", "channel"),
		"signature":      signatureCacheKey("app", "fingerprint", "hash"),
	}
	for name, key := range keys {
		if !strings.Contains(key, version.Version) {
			t.Errorf("%s cache key %q does not embed version.Version", name, key)
		}
	}
}
