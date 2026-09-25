package test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"xprem/internal/bucket"
	"xprem/internal/crypto"
	"xprem/internal/repository"
	"xprem/internal/types"
	"xprem/internal/update"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepublishCASUpdateRemainsDownloadable(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, copyDir(filepath.Join(projectRoot, "test/test-updates"), root))
	t.Setenv("LOCAL_BUCKET_BASE_PATH", root)
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("DB_URL", "")
	bucket.ResetBucketInstance()
	repo := repository.NewBucketUpdateRepository(bucket.GetBucket().UpdateStore)
	ctx := context.Background()
	original, err := repo.GetUpdate(ctx, "test-app-id", "branch-2", "1", "1737455526")
	require.NoError(t, err)
	require.NotNil(t, original)
	originalMapping, err := repo.GetUpdateAssetMapping(ctx, *original)
	require.NoError(t, err)
	require.NotNil(t, originalMapping)
	originalManifest := composeStoredManifest(t, *original)

	w, _, _, r := createRepublishRequest("branch-2", "1", "Authorization", "Bearer expo_test_token", "ios", "republished-commit", original.UpdateId)
	serveThroughRouter(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var republished types.Update
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &republished))
	republishedMapping, err := repo.GetUpdateAssetMapping(ctx, republished)
	require.NoError(t, err)
	require.Equal(t, originalMapping, republishedMapping, "republish must retain the shared blob references")
	republishedManifest := composeStoredManifest(t, republished)
	assert.NotEmpty(t, republishedManifest.Id)
	assert.NotEqual(t, originalManifest.Id, republishedManifest.Id)
	assert.NotEqual(t, original.UpdateId, republished.UpdateId)
	assert.Equal(t, originalManifest.LaunchAsset, republishedManifest.LaunchAsset)
	assert.Equal(t, originalManifest.Assets, republishedManifest.Assets)

	for _, manifest := range []types.UpdateManifest{originalManifest, republishedManifest} {
		for _, asset := range append([]types.ManifestAsset{manifest.LaunchAsset}, manifest.Assets...) {
			assetResponse := httptest.NewRecorder()
			assetRequest := httptest.NewRequest(http.MethodGet, asset.Url, nil)
			assetRequest.Header.Set("expo-app-id", "test-app-id")
			testContainer().ExpoProtocolHandler.HandleAssets(assetResponse, assetRequest)
			require.Equal(t, http.StatusOK, assetResponse.Code)
			hash, err := crypto.CreateHash(assetResponse.Body.Bytes(), "sha256", "base64")
			require.NoError(t, err)
			assert.Equal(t, asset.Hash, crypto.GetBase64URLEncoding(hash))
		}
	}
	assert.Equal(t, originalManifest, composeStoredManifest(t, *original), "republishing must not change the source manifest")
	stored, err := repo.RetrieveUpdateStoredMetadata(ctx, republished)
	require.NoError(t, err)
	assert.Equal(t, "republished-commit", stored.CommitHash)
	assert.Equal(t, republishedManifest.Id, stored.UpdateUUID)
}

// composeStoredManifest follows the stateless manifest path using the actual local bucket.
func composeStoredManifest(t *testing.T, published types.Update) types.UpdateManifest {
	t.Helper()
	ctx := context.Background()
	repo := repository.NewBucketUpdateRepository(bucket.GetBucket().UpdateStore)
	metadata, err := update.GetMetadata(ctx, published)
	require.NoError(t, err)
	stored, err := repo.RetrieveUpdateStoredMetadata(ctx, published)
	require.NoError(t, err)
	mapping, err := repo.GetUpdateAssetMapping(ctx, published)
	require.NoError(t, err)
	manifest, err := update.ComposeUpdateManifest(ctx, &metadata, published, stored, mapping, types.PlatformIOS)
	require.NoError(t, err)
	return manifest
}
