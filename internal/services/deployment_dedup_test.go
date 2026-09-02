package services

import (
	"context"
	"strings"
	"testing"
	"xprem/internal/bucket"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDedupTestHarness wires the rollout harness fakes to the real local bucket,
// which dedup needs to answer whether a blob is already in cas/.
func newDedupTestHarness(t *testing.T) (*DeploymentService, *rolloutTestHarness) {
	t.Helper()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", t.TempDir())
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
		NewBSDiffService(bucket.GetBucket(), nil, h.updateService, h.updateRepo, nil),
	)
	return svc, h
}

func storeBlob(t *testing.T, appId string, file FileUploadItem) {
	t.Helper()
	require.NoError(t, bucket.GetBucket().PutBlob(context.Background(), appId, file.Hash, strings.NewReader(file.Path)))
}

func requestedFilePaths(resp *RequestUploadURLResponse) []string {
	paths := make([]string, 0, len(resp.UploadRequests))
	for _, request := range resp.UploadRequests {
		paths = append(paths, request.FilePath)
	}
	return paths
}

func TestRequestUploadURLs_SkipsBlobsAlreadyStored(t *testing.T) {
	svc, h := newDedupTestHarness(t)
	ctx := context.Background()

	files := hashedUploads("metadata.json", "assets/unchanged", "assets/new")
	storeBlob(t, h.appId, files[2])

	resp, err := svc.RequestUploadURLs(ctx, RequestUploadURLParams{
		RequestID:      "test",
		AppID:          h.appId,
		BranchName:     "main",
		Platform:       "ios",
		RuntimeVersion: "1",
		Files:          files,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{launchAssetPath, "metadata.json", "assets/new"}, requestedFilePaths(resp))
}

// The cas folder is per app, not per branch or platform: a blob uploaded by an
// ios publish is not uploaded again by an android one on another branch.
func TestRequestUploadURLs_DedupIsAppWide(t *testing.T) {
	svc, h := newDedupTestHarness(t)
	ctx := context.Background()

	shared := hashedUpload("assets/shared")
	storeBlob(t, h.appId, shared)

	resp, err := svc.RequestUploadURLs(ctx, RequestUploadURLParams{
		RequestID:      "test",
		AppID:          h.appId,
		BranchName:     "other",
		Platform:       "android",
		RuntimeVersion: "1",
		Files:          append(hashedUploads("metadata.json"), shared),
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{launchAssetPath, "metadata.json"}, requestedFilePaths(resp))
}

func TestRequestUploadURLs_FirstPublishSkipsDedup(t *testing.T) {
	svc, h := newDedupTestHarness(t)
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
	assert.ElementsMatch(t, []string{launchAssetPath, "metadata.json", "assets/aaa"}, requestedFilePaths(resp))
}
