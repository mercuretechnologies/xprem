package services

import (
	"context"
	"strconv"
	"testing"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// publishFiles is one platform's list as eoas builds it: the config files, the
// launch asset, then the assets.
func publishFiles(launch string, assets ...string) []FileUploadItem {
	files := []FileUploadItem{
		configUpload("metadata.json"),
		configUpload("expoConfig.json"),
		roledUpload(launch, "hbc", FileRoleLaunch),
	}
	for _, name := range assets {
		files = append(files, roledUpload(name, "png", FileRoleAsset))
	}
	return files
}

func publishParams(h *rolloutTestHarness, files []FileUploadItem) RequestUploadURLParams {
	return RequestUploadURLParams{
		RequestID:      "test",
		AppID:          h.appId,
		BranchName:     "main",
		Platform:       "ios",
		RuntimeVersion: "1",
		Files:          files,
	}
}

// seedPublished puts a checked update on main/1/ios carrying the mapping a
// previous publish through RequestUploadURLs would have left behind.
func seedPublished(t *testing.T, h *rolloutTestHarness, files []FileUploadItem) {
	t.Helper()
	previous := h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true})
	mapping, err := assetMapping(files)
	require.NoError(t, err)
	require.NoError(t, h.updateRepo.StoreUpdateAssetMapping(context.Background(), previous, mapping))
}

func TestRequestUploadURLs_RefusesAnIdenticalPublish(t *testing.T) {
	svc, h := newDedupTestHarness(t)
	seedPublished(t, h, publishFiles("bundle-v1", "assets/a", "assets/b"))

	_, err := svc.RequestUploadURLs(context.Background(), publishParams(h, publishFiles("bundle-v1", "assets/b", "assets/a")))
	assert.ErrorIs(t, err, ErrNoChangesDetected)
}

func TestRequestUploadURLs_AcceptsAChangedBundle(t *testing.T) {
	svc, h := newDedupTestHarness(t)
	seedPublished(t, h, publishFiles("bundle-v1", "assets/a"))

	resp, err := svc.RequestUploadURLs(context.Background(), publishParams(h, publishFiles("bundle-v2", "assets/a")))
	require.NoError(t, err)
	assert.NotZero(t, resp.UpdateID)
}

// An update published before mappings existed reads back nil. It must let the
// publish through rather than block it: the folder layout still serves it.
func TestRequestUploadURLs_PublishesWhenTheLatestHasNoMapping(t *testing.T) {
	svc, h := newDedupTestHarness(t)
	h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true})

	_, err := svc.RequestUploadURLs(context.Background(), publishParams(h, publishFiles("bundle-v1", "assets/a")))
	require.NoError(t, err)
}

// A file list with no bundle cannot produce a manifest. Refusing here saves the
// CLI from uploading everything only to be rejected at markUpdateAsUploaded.
func TestRequestUploadURLs_RefusesAFileListWithoutALaunchAsset(t *testing.T) {
	svc, h := newDedupTestHarness(t)

	_, err := svc.RequestUploadURLs(context.Background(), publishParams(h, []FileUploadItem{configUpload("metadata.json")}))
	assert.ErrorIs(t, err, ErrLaunchAssetRequired)
}

func TestRequestUploadURLs_RefusesTwoLaunchAssets(t *testing.T) {
	svc, h := newDedupTestHarness(t)
	files := append(publishFiles("bundle-ios"), roledUpload("bundle-android", "hbc", FileRoleLaunch))

	_, err := svc.RequestUploadURLs(context.Background(), publishParams(h, files))
	assert.ErrorIs(t, err, ErrLaunchAssetRequired)
}

func TestRequestUploadURLs_StoresTheShapedMapping(t *testing.T) {
	svc, h := newDedupTestHarness(t)
	ctx := context.Background()

	resp, err := svc.RequestUploadURLs(ctx, publishParams(h, publishFiles("bundle-v1", "assets/a")))
	require.NoError(t, err)

	stored, err := h.updateRepo.GetUpdateAssetMapping(ctx, types.Update{
		AppId:    h.appId,
		Branch:   "main",
		UpdateId: strconv.FormatInt(resp.UpdateID, 10),
	})
	require.NoError(t, err)
	require.NotNil(t, stored)

	assert.Equal(t, hashedUpload("bundle-v1").Hash, stored.LaunchAsset.Hash)
	assert.Equal(t, "bundle-v1-key", stored.LaunchAsset.Key)
	assert.Equal(t, ".bundle", stored.LaunchAsset.FileExtension)
	assert.Equal(t, "application/javascript", stored.LaunchAsset.ContentType)

	require.Len(t, stored.Assets, 1)
	assert.Equal(t, ".png", stored.Assets[0].FileExtension)
	assert.Contains(t, stored.Assets[0].ContentType, "image/png")
}
