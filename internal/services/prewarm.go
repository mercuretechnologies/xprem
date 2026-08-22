package services

import (
	"xprem/internal/types"

	"context"
	"log"
	update2 "xprem/internal/update"
)

// PreWarmManifestCache populates the manifest cache layers for the given
// appId/branch/runtimeVersion/platform combination. It is intended to be
// called as a goroutine after MarkUpdateAsChecked so the first client
// request hits warm caches instead of rebuilding everything from scratch.
func PreWarmManifestCache(updateService *UpdateService, appId string, branch string, runtimeVersion string, platform types.Platform) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PreWarm] panic recovered for app=%s branch=%s rv=%s platform=%s: %v", appId, branch, runtimeVersion, platform, r)
		}
	}()

	ctx := context.Background()
	latestUpdate, err := updateService.GetLatestUpdate(ctx, appId, branch, runtimeVersion, platform)
	if err != nil {
		log.Printf("[PreWarm] error getting latest update for app=%s branch=%s rv=%s platform=%s: %v", appId, branch, runtimeVersion, platform, err)
		return
	}
	if latestUpdate == nil {
		return
	}

	metadata, err := update2.GetMetadata(*latestUpdate)
	if err != nil {
		log.Printf("[PreWarm] error getting metadata for update=%s: %v", latestUpdate.UpdateId, err)
		return
	}

	_, err = update2.ComposeUpdateManifest(&metadata, *latestUpdate, platform)
	if err != nil {
		log.Printf("[PreWarm] error composing manifest for update=%s platform=%s: %v", latestUpdate.UpdateId, platform, err)
		return
	}

	log.Printf("[PreWarm] successfully pre-warmed cache for branch=%s rv=%s platform=%s", branch, runtimeVersion, platform)
}

// PreWarmControlManifest composes the manifest of the control update behind an active
// per-update rollout. The manifest cache is per updateId, so warming only the rollout
// update would leave the first out-of-bucket client to re-hash every control asset.
// No-op when the latest update carries no active rollout or no control.
func PreWarmControlManifest(updateService *UpdateService, appId string, branch string, runtimeVersion string, platform types.Platform) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PreWarm] panic recovered for control app=%s branch=%s rv=%s platform=%s: %v", appId, branch, runtimeVersion, platform, r)
		}
	}()

	ctx := context.Background()
	envelope, err := updateService.getLatestUpdateEnvelope(ctx, appId, branch, runtimeVersion, platform)
	if err != nil {
		log.Printf("[PreWarm] error getting latest update envelope for app=%s branch=%s rv=%s platform=%s: %v", appId, branch, runtimeVersion, platform, err)
		return
	}
	if envelope == nil || envelope.RolloutPercentage == nil || envelope.Control == nil {
		return
	}

	metadata, err := update2.GetMetadata(*envelope.Control)
	if err != nil {
		log.Printf("[PreWarm] error getting metadata for control update=%s: %v", envelope.Control.UpdateId, err)
		return
	}

	_, err = update2.ComposeUpdateManifest(&metadata, *envelope.Control, platform)
	if err != nil {
		log.Printf("[PreWarm] error composing manifest for control update=%s platform=%s: %v", envelope.Control.UpdateId, platform, err)
		return
	}

	log.Printf("[PreWarm] successfully pre-warmed control manifest for branch=%s rv=%s platform=%s", branch, runtimeVersion, platform)
}
