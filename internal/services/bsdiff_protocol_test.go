package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"testing"

	"xprem/internal/bucket"
	"xprem/internal/cdn"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	patchTestCurrentUUID   = "11111111-1111-1111-1111-111111111111"
	patchTestRequestedUUID = "22222222-2222-2222-2222-222222222222"
	patchTestOtherUUID     = "33333333-3333-3333-3333-333333333333"
)

var (
	patchTestBundle = []byte("full bundle")
	patchTestPatch  = []byte("bsdiff patch")
)

// fakePatchBucket holds one blob and the patches keyed by their object key.
type fakePatchBucket struct {
	fakeRolloutBucket
	patches     map[string][]byte
	existsCalls int
	readCalls   int
	readErr     error
}

func (b *fakePatchBucket) GetBlob(context.Context, string, string) (*types.BucketFile, error) {
	return &types.BucketFile{Reader: io.NopCloser(bytes.NewReader(patchTestBundle))}, nil
}

func (b *fakePatchBucket) BSDiffExists(_ context.Context, appId, branch, target, source string) (bool, error) {
	b.existsCalls++
	_, ok := b.patches[bucket.BSDiffObjectKey(appId, branch, target, source)]
	return ok, nil
}

func (b *fakePatchBucket) GetBSDiff(_ context.Context, appId, branch, target, source string) (*types.BucketFile, error) {
	b.readCalls++
	if b.readErr != nil {
		return nil, b.readErr
	}
	body, ok := b.patches[bucket.BSDiffObjectKey(appId, branch, target, source)]
	if !ok {
		return nil, nil
	}
	return &types.BucketFile{Reader: io.NopCloser(bytes.NewReader(body))}, nil
}

// patchHarness seeds two checked updates on main plus one on another branch,
// with a patch stored from the first main update to the second, and maps the
// production channel to main.
func patchHarness(t *testing.T) (*rolloutTestHarness, *ExpoProtocolService, *fakePatchBucket) {
	t.Helper()
	t.Setenv("DB_URL", "postgres://bsdiff-protocol-tests")
	t.Setenv("BUNDLE_DIFFING", "true")
	t.Setenv("BUNDLE_DIFFING_CDN_REDIRECT", "")
	t.Setenv("STORAGE_MODE", "")
	t.Setenv("S3_BUCKET_NAME", "")
	t.Setenv("CDN_BASE_URL", "")
	t.Setenv("BUCKET_KEY_PREFIX", "")
	cdn.ResetCDNInstance()
	t.Cleanup(cdn.ResetCDNInstance)
	h := newRolloutTestHarness(t)
	h.channelRepo.mappings["production"] = &types.ChannelResolution{Id: "1", BranchName: "main"}
	h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true, uuid: patchTestCurrentUUID})
	h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 200, checked: true, uuid: patchTestRequestedUUID})
	h.seed(seedRow{branch: "internal", rtv: "1", platform: "ios", id: 300, checked: true, uuid: patchTestOtherUUID})
	patchBucket := &fakePatchBucket{patches: map[string][]byte{
		bucket.BSDiffObjectKey(h.appId, "main", patchTestRequestedUUID, patchTestCurrentUUID): patchTestPatch,
	}}
	service := NewExpoProtocolService(fakeAppRepo{}, h.channelRepo, h.updateRepo, h.updateService, DefaultBranchRules(), patchBucket)
	return h, service, patchBucket
}

// withGenericCDN points the resolved CDN at a base URL, the way an S3 bucket
// behind a CDN_BASE_URL edge is deployed.
func withGenericCDN(t *testing.T, keyPrefix string) {
	t.Helper()
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_BUCKET_NAME", "test-bucket")
	t.Setenv("CDN_BASE_URL", "https://cdn.example.com")
	t.Setenv("BUCKET_KEY_PREFIX", keyPrefix)
	cdn.ResetCDNInstance()
}

func assertPatchResult(t *testing.T, result *ExpoAssetResult) {
	t.Helper()
	assert.Equal(t, patchTestPatch, result.Body)
	assert.Equal(t, 200, result.StatusCode)
	assert.Equal(t, "application/octet-stream", result.ContentType)
	assert.True(t, result.Uncompressed)
	assert.Equal(t, "bsdiff", result.Headers["im"])
	assert.Equal(t, patchTestCurrentUUID, result.Headers["expo-base-update-id"])
	assert.Equal(t, "private, no-store", result.Headers["Cache-Control"])
	assert.Equal(t, "A-IM, Expo-Current-Update-ID", result.Headers["Vary"])
	assert.Empty(t, result.RedirectToURL)
}

func assertFullBundleResult(t *testing.T, result *ExpoAssetResult) {
	t.Helper()
	assert.Equal(t, patchTestBundle, result.Body)
	assert.Equal(t, 200, result.StatusCode)
	assert.NotContains(t, result.Headers, "im")
	assert.NotContains(t, result.Headers, "expo-base-update-id")
	assert.Empty(t, result.RedirectToURL)
}

func patchParams(h *rolloutTestHarness) AssetResolutionParams {
	digest := sha256.Sum256(patchTestBundle)
	return AssetResolutionParams{
		RequestID:           "test",
		AppID:               h.appId,
		ChannelName:         "production",
		RuntimeVersion:      "1",
		Platform:            "ios",
		Hash:                base64.RawURLEncoding.EncodeToString(digest[:]),
		Extension:           "bundle",
		AIM:                 "bsdiff",
		ExpoCurrentUpdateId: patchTestCurrentUUID,
		RequestedUpdateID:   patchTestRequestedUUID,
	}
}

func TestResolveAssetServesTheStoredPatch(t *testing.T) {
	for _, aim := range []string{"bsdiff", "BSDIFF", "gzip, bsdiff;q=1.0"} {
		t.Run(aim, func(t *testing.T) {
			h, service, _ := patchHarness(t)
			params := patchParams(h)
			params.AIM = aim

			result, err := service.ResolveAsset(context.Background(), params)
			require.NoError(t, err)
			assertPatchResult(t, result)
		})
	}
}

func TestResolveAssetPatchRedirectsToTheCDN(t *testing.T) {
	for _, keyPrefix := range []string{"", "prefix/"} {
		t.Run("key prefix "+keyPrefix, func(t *testing.T) {
			h, service, patchBucket := patchHarness(t)
			t.Setenv("BUNDLE_DIFFING_CDN_REDIRECT", "true")
			withGenericCDN(t, keyPrefix)

			result, err := service.ResolveAsset(context.Background(), patchParams(h))
			require.NoError(t, err)

			assert.Equal(t, "https://cdn.example.com/"+keyPrefix+h.appId+"/bsdiff/main/"+patchTestRequestedUUID+"/"+patchTestCurrentUUID, result.RedirectToURL)
			assert.Empty(t, result.Body)
			assert.Equal(t, 0, patchBucket.readCalls, "a redirect must not read the patch")
			assert.Equal(t, 1, patchBucket.existsCalls, "a redirect to a missing object would fail the download")
		})
	}
}

func TestResolveAssetPatchStaysOnTheServerWhenRedirectIsOff(t *testing.T) {
	h, service, _ := patchHarness(t)
	withGenericCDN(t, "")

	result, err := service.ResolveAsset(context.Background(), patchParams(h))
	require.NoError(t, err)
	assertPatchResult(t, result)

	// The same CDN still serves the full bundle when no patch applies.
	params := patchParams(h)
	params.AIM = ""
	result, err = service.ResolveAsset(context.Background(), params)
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example.com/"+h.appId+"/cas/"+params.Hash, result.RedirectToURL)
}

// A direct-to-storage CDN cannot add the patch headers, so the flag falls
// back to serving the patch from the server rather than breaking the download.
func TestResolveAssetPatchServesDirectlyWhenTheCDNCannotRedirect(t *testing.T) {
	h, service, _ := patchHarness(t)
	t.Setenv("BUNDLE_DIFFING_CDN_REDIRECT", "true")
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_BUCKET_NAME", "test-bucket")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
	cdn.ResetCDNInstance()
	require.Equal(t, "s3-direct", cdn.ResolvedType())

	result, err := service.ResolveAsset(context.Background(), patchParams(h))
	require.NoError(t, err)
	assertPatchResult(t, result)
}

func TestResolveAssetPatchReadErrorFallsBackToTheFullBundle(t *testing.T) {
	h, service, patchBucket := patchHarness(t)
	patchBucket.readErr = errors.New("bucket down")

	result, err := service.ResolveAsset(context.Background(), patchParams(h))
	require.NoError(t, err)
	assertFullBundleResult(t, result)
}

func TestResolveAssetCachesThePatchLookup(t *testing.T) {
	h, service, patchBucket := patchHarness(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		result, err := service.ResolveAsset(ctx, patchParams(h))
		require.NoError(t, err)
		assertPatchResult(t, result)
	}
	assert.Equal(t, 1, patchBucket.existsCalls)
	assert.Equal(t, 3, patchBucket.readCalls, "the patch body itself is not cached")
	assert.Equal(t, 2, h.updateRepo.uuidLookups(), "current and requested updates are each read once")

	// A missing patch is remembered too: storing it afterwards does not
	// change the answer until the negative entry expires.
	params := patchParams(h)
	params.ExpoCurrentUpdateId, params.RequestedUpdateID = patchTestRequestedUUID, patchTestCurrentUUID
	result, err := service.ResolveAsset(ctx, params)
	require.NoError(t, err)
	assertFullBundleResult(t, result)
	patchBucket.patches[bucket.BSDiffObjectKey(h.appId, "main", patchTestCurrentUUID, patchTestRequestedUUID)] = patchTestPatch
	result, err = service.ResolveAsset(ctx, params)
	require.NoError(t, err)
	assertFullBundleResult(t, result)
	assert.Equal(t, 2, patchBucket.existsCalls)
}

func TestResolveAssetFallsBackToTheFullBundle(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		mutate func(h *rolloutTestHarness, params *AssetResolutionParams)
	}{
		{"client does not accept bsdiff", nil, func(_ *rolloutTestHarness, p *AssetResolutionParams) { p.AIM = "" }},
		{"client accepts another encoding", nil, func(_ *rolloutTestHarness, p *AssetResolutionParams) { p.AIM = "gzip" }},
		{"diffing is disabled", map[string]string{"BUNDLE_DIFFING": "false"}, func(_ *rolloutTestHarness, _ *AssetResolutionParams) {}},
		{"no current update header", nil, func(_ *rolloutTestHarness, p *AssetResolutionParams) { p.ExpoCurrentUpdateId = "" }},
		{"current update is not a uuid", nil, func(_ *rolloutTestHarness, p *AssetResolutionParams) { p.ExpoCurrentUpdateId = "100" }},
		{"current and requested are the same update", nil, func(_ *rolloutTestHarness, p *AssetResolutionParams) { p.RequestedUpdateID = patchTestCurrentUUID }},
		{"unknown current update", nil, func(_ *rolloutTestHarness, p *AssetResolutionParams) {
			p.ExpoCurrentUpdateId = "44444444-4444-4444-4444-444444444444"
		}},
		{"updates on different branches", nil, func(_ *rolloutTestHarness, p *AssetResolutionParams) { p.RequestedUpdateID = patchTestOtherUUID }},
		{"updates on different runtime versions", nil, func(h *rolloutTestHarness, p *AssetResolutionParams) {
			h.seed(seedRow{branch: "main", rtv: "2", platform: "ios", id: 400, checked: true, uuid: "55555555-5555-5555-5555-555555555555"})
			p.RequestedUpdateID = "55555555-5555-5555-5555-555555555555"
		}},
		{"requested update is not checked", nil, func(h *rolloutTestHarness, p *AssetResolutionParams) {
			h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 500, checked: false, uuid: "66666666-6666-6666-6666-666666666666"})
			p.RequestedUpdateID = "66666666-6666-6666-6666-666666666666"
		}},
		{"no patch stored for the pair", nil, func(_ *rolloutTestHarness, p *AssetResolutionParams) {
			p.ExpoCurrentUpdateId, p.RequestedUpdateID = patchTestRequestedUUID, patchTestCurrentUUID
		}},
		{"channel does not serve the branch", nil, func(h *rolloutTestHarness, _ *AssetResolutionParams) {
			h.channelRepo.mappings["production"] = &types.ChannelResolution{Id: "1", BranchName: "internal"}
		}},
		{"unknown channel", nil, func(_ *rolloutTestHarness, p *AssetResolutionParams) { p.ChannelName = "nope" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, service, _ := patchHarness(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			params := patchParams(h)
			tc.mutate(h, &params)

			result, err := service.ResolveAsset(context.Background(), params)
			require.NoError(t, err)
			assertFullBundleResult(t, result)
		})
	}
}
