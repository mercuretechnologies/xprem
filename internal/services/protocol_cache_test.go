package services

import (
	"context"
	"strings"
	"testing"
	"xprem/config"
	cache2 "xprem/internal/cache"
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

type countingChannelRepo struct {
	*fakeChannelRepo
	calls int
	// surfingReads counts branch-surfing lookups, which is what tells a cache
	// hit from a miss; surfingErr makes the repository fail.
	surfingReads int
	surfingErr   error
}

func (r *countingChannelRepo) GetChannelBranchMapping(ctx context.Context, appId, channelName string) (*types.ChannelResolution, error) {
	r.calls++
	return r.fakeChannelRepo.GetChannelBranchMapping(ctx, appId, channelName)
}

func (r *countingChannelRepo) GetBranchSurfing(ctx context.Context, appId, channelName string) (*types.BranchSurfing, error) {
	r.surfingReads++
	if r.surfingErr != nil {
		return nil, r.surfingErr
	}
	return r.fakeChannelRepo.GetBranchSurfing(ctx, appId, channelName)
}

func TestChannelBranchMappingCache(t *testing.T) {
	// The service-layer mapping cache only exists on the control plane; in
	// stateless mode the expo provider's own cache is the single layer.
	t.Setenv("DB_URL", "postgres://stub")
	repo := &countingChannelRepo{fakeChannelRepo: &fakeChannelRepo{mappings: map[string]*types.ChannelResolution{
		"production": {Id: "1", BranchName: "main", Rollout: &types.ChannelRolloutInfo{ID: "r1", BranchName: "canary", Percentage: 30}},
	}}}
	protocolService := NewExpoProtocolService(fakeAppRepo{}, repo, nil, nil, nil, nil)
	ctx := context.Background()
	appId := "channel-mapping-cache-test"
	cache2.GetCache().Delete(channelMappingCacheKey(appId, "production"))

	first, err := protocolService.channelBranchMapping(ctx, appId, "production")
	if err != nil || first == nil {
		t.Fatalf("first read: mapping=%v err=%v", first, err)
	}
	if repo.calls != 1 {
		t.Fatalf("first read must hit the repository once, got %d calls", repo.calls)
	}

	second, err := protocolService.channelBranchMapping(ctx, appId, "production")
	if err != nil || second == nil {
		t.Fatalf("second read: mapping=%v err=%v", second, err)
	}
	if repo.calls != 1 {
		t.Fatalf("second read must be served from the cache, got %d repository calls", repo.calls)
	}
	if second.BranchName != "main" || second.Rollout == nil ||
		second.Rollout.ID != "r1" || second.Rollout.BranchName != "canary" || second.Rollout.Percentage != 30 {
		t.Fatalf("cached mapping lost data through the round trip: %+v rollout=%+v", second, second.Rollout)
	}

	// A remap becomes visible once the entry expires (simulated by the Delete).
	repo.mappings["production"].BranchName = "hotfix"
	cache2.GetCache().Delete(channelMappingCacheKey(appId, "production"))
	third, err := protocolService.channelBranchMapping(ctx, appId, "production")
	if err != nil || third == nil {
		t.Fatalf("post-remap read: mapping=%v err=%v", third, err)
	}
	if third.BranchName != "hotfix" || repo.calls != 2 {
		t.Fatalf("expired entry must re-read the repository: branch=%s calls=%d", third.BranchName, repo.calls)
	}
}

func TestChannelBranchMappingNilNeverCached(t *testing.T) {
	repo := &countingChannelRepo{fakeChannelRepo: &fakeChannelRepo{mappings: map[string]*types.ChannelResolution{}}}
	protocolService := NewExpoProtocolService(fakeAppRepo{}, repo, nil, nil, nil, nil)
	ctx := context.Background()
	appId := "channel-mapping-nil-test"
	cache2.GetCache().Delete(channelMappingCacheKey(appId, "ghost"))

	for i := 1; i <= 2; i++ {
		mapping, err := protocolService.channelBranchMapping(ctx, appId, "ghost")
		if err != nil || mapping != nil {
			t.Fatalf("unknown channel must resolve to (nil, nil), got mapping=%v err=%v", mapping, err)
		}
		if repo.calls != i {
			t.Fatalf("a nil mapping must never be cached: read %d made %d repository calls", i, repo.calls)
		}
	}
}

// An app config holding a plaintext secret must be served but never stored:
// a shared cache is not a place for an access token or an unencrypted key.
func TestAppConfigCarryingAPlaintextSecretIsNeverCached(t *testing.T) {
	for name, appConfig := range map[string]config.AppConfig{
		"access token":     {Id: "app", AccessToken: "expo-token"},
		"env private key":  {Id: "app", Keys: config.KeysConfig{Mode: config.KeysModeEnvironment, PrivateB64: "cHJpdmF0ZQ=="}},
		"sealed key is ok": {Id: "app", Keys: config.KeysConfig{Mode: config.KeysModeDatabase, SealedPrivateKey: "sealed"}},
	} {
		t.Run(name, func(t *testing.T) {
			secret := carriesPlaintextSecret(appConfig)
			if name == "sealed key is ok" {
				if secret {
					t.Fatal("a sealed key is encrypted with the master key and may be cached")
				}
				return
			}
			if !secret {
				t.Fatalf("%s must keep the config out of the cache", name)
			}
		})
	}
}
