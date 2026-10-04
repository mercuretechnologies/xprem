package services

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"xprem/internal/bucket"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSourcemapTestHarness is the dedup harness with source map uploads on,
// their store sharing the updates directory.
func newSourcemapTestHarness(t *testing.T) (*DeploymentService, *rolloutTestHarness, *bucket.SourcemapStore) {
	t.Helper()
	svc, h := newDedupTestHarness(t)
	t.Setenv("DB_URL", "postgres://localhost/xprem")
	t.Setenv("UPLOAD_SOURCEMAPS", "true")
	t.Setenv("LOCAL_SOURCEMAPS_BASE_PATH", os.Getenv("LOCAL_BUCKET_BASE_PATH"))
	store, err := bucket.OpenSourcemapStore()
	require.NoError(t, err)
	// A nil *SourcemapStore in the interface would pass the service's nil check.
	require.NotNil(t, store)
	svc.SetSourcemapStore(store)
	return svc, h, store
}

func sourcemapUpload() SourcemapUploadItem {
	file := roledUpload(launchAssetPath+".map", "map", FileRoleAsset)
	return SourcemapUploadItem{Path: file.Path, Hash: file.Hash}
}

func TestRequestUploadURLs_RequestsTheSourcemapAndRecordsIt(t *testing.T) {
	svc, h, _ := newSourcemapTestHarness(t)
	ctx := context.Background()
	sourcemap := sourcemapUpload()

	params := publishParams(h, hashedUploads("metadata.json"))
	params.Sourcemap = &sourcemap
	resp, err := svc.RequestUploadURLs(ctx, params)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{launchAssetPath, "metadata.json", sourcemap.Path}, requestedFilePaths(resp))

	update, err := h.updateRepo.GetUpdate(ctx, h.appId, "main", "1", strconv.FormatInt(resp.UpdateID, 10))
	require.NoError(t, err)
	hash, err := h.updateRepo.GetUpdateSourcemapHash(ctx, *update)
	require.NoError(t, err)
	require.NotNil(t, hash)
	assert.Equal(t, sourcemap.Hash, *hash)

	mapping, err := h.updateRepo.GetUpdateAssetMapping(ctx, *update)
	require.NoError(t, err)
	assert.Len(t, mapping.Assets, 1, "the source map is not an asset of the manifest")
}

func TestRequestUploadURLs_SkipsASourcemapAlreadyStored(t *testing.T) {
	svc, h, store := newSourcemapTestHarness(t)
	ctx := context.Background()
	sourcemap := sourcemapUpload()
	require.NoError(t, store.Put(ctx, h.appId, sourcemap.Hash, strings.NewReader(sourcemap.Path)))

	params := publishParams(h, hashedUploads("metadata.json"))
	params.Sourcemap = &sourcemap
	resp, err := svc.RequestUploadURLs(ctx, params)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{launchAssetPath, "metadata.json"}, requestedFilePaths(resp))

	update, err := h.updateRepo.GetUpdate(ctx, h.appId, "main", "1", strconv.FormatInt(resp.UpdateID, 10))
	require.NoError(t, err)
	hash, err := h.updateRepo.GetUpdateSourcemapHash(ctx, *update)
	require.NoError(t, err)
	require.NotNil(t, hash)
	assert.Equal(t, sourcemap.Hash, *hash)
	_, err = svc.verifySourcemapUploaded(ctx, *update)
	require.NoError(t, err)
}

func TestRequestUploadURLs_IgnoresTheSourcemapWhenUploadsAreOff(t *testing.T) {
	svc, h := newDedupTestHarness(t)
	ctx := context.Background()
	sourcemap := sourcemapUpload()

	params := publishParams(h, hashedUploads("metadata.json"))
	params.Sourcemap = &sourcemap
	resp, err := svc.RequestUploadURLs(ctx, params)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{launchAssetPath, "metadata.json"}, requestedFilePaths(resp))

	update, err := h.updateRepo.GetUpdate(ctx, h.appId, "main", "1", strconv.FormatInt(resp.UpdateID, 10))
	require.NoError(t, err)
	hash, err := h.updateRepo.GetUpdateSourcemapHash(ctx, *update)
	require.NoError(t, err)
	assert.Nil(t, hash)
}

// A declared source map that never landed fails the publish, like a missing blob.
func TestVerifySourcemapUploaded(t *testing.T) {
	svc, h, store := newSourcemapTestHarness(t)
	ctx := context.Background()
	sourcemap := sourcemapUpload()

	params := publishParams(h, hashedUploads("metadata.json"))
	params.Sourcemap = &sourcemap
	resp, err := svc.RequestUploadURLs(ctx, params)
	require.NoError(t, err)
	update, err := h.updateRepo.GetUpdate(ctx, h.appId, "main", "1", strconv.FormatInt(resp.UpdateID, 10))
	require.NoError(t, err)

	_, err = svc.verifySourcemapUploaded(ctx, *update)
	require.ErrorContains(t, err, "missing sourcemap")

	require.NoError(t, store.Put(ctx, h.appId, sourcemap.Hash, strings.NewReader(sourcemap.Path)))
	_, err = svc.verifySourcemapUploaded(ctx, *update)
	require.NoError(t, err)
}

func TestVerifySourcemapUploaded_NothingDeclared(t *testing.T) {
	svc, h := newDedupTestHarness(t)
	ctx := context.Background()

	resp, err := svc.RequestUploadURLs(ctx, publishParams(h, hashedUploads("metadata.json")))
	require.NoError(t, err)
	update, err := h.updateRepo.GetUpdate(ctx, h.appId, "main", "1", strconv.FormatInt(resp.UpdateID, 10))
	require.NoError(t, err)
	_, err = svc.verifySourcemapUploaded(ctx, *update)
	require.NoError(t, err)
}
