package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"xprem/internal/bucket"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDedupTestHarness wires the rollout harness fakes to the real local bucket,
// which dedup needs both to read the previous update's metadata.json (through
// the bucket singleton) and to copy files.
func newDedupTestHarness(t *testing.T) (*DeploymentService, *rolloutTestHarness, string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", base)
	t.Setenv("BASE_URL", "http://localhost:3000")
	t.Setenv("JWT_SECRET", "test_jwt_secret")
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)

	h := newRolloutTestHarness(t)
	svc := NewDeploymentService(
		NewBranchService(fakeBranchRepo{}, h.channelRepo, h.updateRepo, h.rolloutRepo, fakeRolloutBucket{}),
		h.updateService,
		h.updateRepo,
		bucket.GetBucket(),
	)
	return svc, h, base
}

func writePreviousUpdateFiles(t *testing.T, base, appId string, update types.Update, metadata types.MetadataObject, assetContents map[string]string) {
	t.Helper()
	dir := filepath.Join(base, appId, update.Branch, update.RuntimeVersion, update.UpdateId)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	raw, err := json.Marshal(metadata)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.json"), raw, 0o644))
	for name, content := range assetContents {
		filePath := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(filePath), 0o755))
		require.NoError(t, os.WriteFile(filePath, []byte(content), 0o644))
	}
}

func requestedFilePaths(resp *RequestUploadURLResponse) []string {
	paths := make([]string, 0, len(resp.UploadRequests))
	for _, request := range resp.UploadRequests {
		paths = append(paths, request.FilePath)
	}
	return paths
}

func TestRequestUploadURLs_DedupsUnchangedAssets(t *testing.T) {
	svc, h, base := newDedupTestHarness(t)
	ctx := context.Background()

	prev := h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true})
	// "assets/ghost" is listed in the previous metadata but absent from the
	// bucket: its copy fails, so it must stay in the upload list.
	writePreviousUpdateFiles(t, base, h.appId, prev, types.MetadataObject{
		Version: 0,
		Bundler: "metro",
		FileMetadata: types.FileMetadata{
			IOS: types.PlatformMetadata{
				Bundle: "bundles/ios-old.hbc",
				Assets: []types.Asset{
					{Path: "assets/unchanged", Ext: "png"},
					{Path: "assets/ghost", Ext: "png"},
				},
			},
		},
	}, map[string]string{"assets/unchanged": "png-bytes"})

	resp, err := svc.RequestUploadURLs(ctx, RequestUploadURLParams{
		RequestID:      "test",
		AppID:          h.appId,
		BranchName:     "main",
		Platform:       "ios",
		RuntimeVersion: "1",
		Files: hashedUploads(
			"metadata.json",
			"expoConfig.json",
			"bundles/ios-new.hbc",
			"assets/unchanged",
			"assets/ghost",
		),
	})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{
		"metadata.json",
		"expoConfig.json",
		"bundles/ios-new.hbc",
		"assets/ghost",
	}, requestedFilePaths(resp))

	copied, err := os.ReadFile(filepath.Join(base, h.appId, "main", "1", strconv.FormatInt(resp.UpdateID, 10), "assets", "unchanged"))
	require.NoError(t, err)
	assert.Equal(t, "png-bytes", string(copied))
}

func TestRequestUploadURLs_FirstPublishSkipsDedup(t *testing.T) {
	svc, h, _ := newDedupTestHarness(t)
	ctx := context.Background()

	resp, err := svc.RequestUploadURLs(ctx, RequestUploadURLParams{
		RequestID:      "test",
		AppID:          h.appId,
		BranchName:     "main",
		Platform:       "ios",
		RuntimeVersion: "1",
		Files:          hashedUploads("metadata.json", "assets/aaa"),
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"metadata.json", "assets/aaa"}, requestedFilePaths(resp))
}

func TestRequestUploadURLs_DedupIgnoresOtherPlatformUpdates(t *testing.T) {
	svc, h, base := newDedupTestHarness(t)
	ctx := context.Background()

	prev := h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true})
	writePreviousUpdateFiles(t, base, h.appId, prev, types.MetadataObject{
		Version: 0,
		Bundler: "metro",
		FileMetadata: types.FileMetadata{
			IOS: types.PlatformMetadata{
				Bundle: "bundles/ios-old.hbc",
				Assets: []types.Asset{{Path: "assets/shared", Ext: "png"}},
			},
		},
	}, map[string]string{"assets/shared": "png-bytes"})

	// No android update exists yet, so an android publish must upload
	// everything even though an ios update holds the same asset.
	resp, err := svc.RequestUploadURLs(ctx, RequestUploadURLParams{
		RequestID:      "test",
		AppID:          h.appId,
		BranchName:     "main",
		Platform:       "android",
		RuntimeVersion: "1",
		Files:          hashedUploads("metadata.json", "assets/shared"),
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"metadata.json", "assets/shared"}, requestedFilePaths(resp))
}
