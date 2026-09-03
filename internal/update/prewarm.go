package update

import (
	"expo-open-ota/internal/types"
	"log"
)

// PreWarmUpdateManifestCache populates the metadata/manifest cache layers for a
// known update. It intentionally does not resolve the latest update or write the
// lastUpdate cache: callers that just marked an update as checked already know
// which update should be warmed, and resolving latest here can race with stale
// lastUpdate entries.
func PreWarmUpdateManifestCache(update types.Update, platform string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PreWarm] panic recovered for update=%s platform=%s: %v", update.UpdateId, platform, r)
		}
	}()

	metadata, err := GetMetadata(update)
	if err != nil {
		log.Printf("[PreWarm] error getting metadata for update=%s: %v", update.UpdateId, err)
		return
	}

	_, err = ComposeUpdateManifest(&metadata, update, platform)
	if err != nil {
		log.Printf("[PreWarm] error composing manifest for update=%s platform=%s: %v", update.UpdateId, platform, err)
		return
	}

	log.Printf("[PreWarm] successfully pre-warmed manifest cache for update=%s platform=%s", update.UpdateId, platform)
}

// PreWarmManifestCache resolves the latest checked update for the given
// branch/runtimeVersion/platform combination and warms its manifest cache.
func PreWarmManifestCache(branch, runtimeVersion, platform string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PreWarm] panic recovered for branch=%s rv=%s platform=%s: %v", branch, runtimeVersion, platform, r)
		}
	}()

	latestUpdate, err := GetLatestUpdateBundlePathForRuntimeVersion(branch, runtimeVersion, platform)
	if err != nil {
		log.Printf("[PreWarm] error getting latest update for branch=%s rv=%s platform=%s: %v", branch, runtimeVersion, platform, err)
		return
	}
	if latestUpdate == nil {
		return
	}

	metadata, err := GetMetadata(*latestUpdate)
	if err != nil {
		log.Printf("[PreWarm] error getting metadata for update=%s: %v", latestUpdate.UpdateId, err)
		return
	}

	_, err = ComposeUpdateManifest(&metadata, *latestUpdate, platform)
	if err != nil {
		log.Printf("[PreWarm] error composing manifest for update=%s platform=%s: %v", latestUpdate.UpdateId, platform, err)
		return
	}

	log.Printf("[PreWarm] successfully pre-warmed cache for branch=%s rv=%s platform=%s", branch, runtimeVersion, platform)
}
