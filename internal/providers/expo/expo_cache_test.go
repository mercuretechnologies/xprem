package expo

import (
	"strings"
	"testing"
	"xprem/internal/version"
)

// Serialized-payload cache keys must embed the release version, see the
// matching test in internal/services.
func TestProviderCacheKeysEmbedReleaseVersion(t *testing.T) {
	keys := map[string]string{
		"userAccountToken":   userAccountTokenCacheKey("token"),
		"userAccountSession": userAccountSessionCacheKey("secret"),
		"selfUsername":       selfUsernameCacheKey("token"),
		"appName":            appNameCacheKey("app", "token"),
		"channelMapping":     channelMappingCacheKey("app", "channel"),
	}
	for name, key := range keys {
		if !strings.Contains(key, version.Version) {
			t.Errorf("%s cache key %q does not embed version.Version", name, key)
		}
	}
}
