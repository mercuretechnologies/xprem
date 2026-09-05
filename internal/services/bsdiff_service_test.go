package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"testing"

	"xprem/internal/bsdiff"
	"xprem/internal/bucket"
	"xprem/internal/types"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePatchRepo records the bookkeeping writes of a patch job in order.
type fakePatchRepo struct {
	events []string
}

func (r *fakePatchRepo) MarkPending(_ context.Context, _, _, target, source string) error {
	r.events = append(r.events, fmt.Sprintf("pending %s<-%s", target, source))
	return nil
}

func (r *fakePatchRepo) MarkRunning(_ context.Context, _, _, target, source string) error {
	r.events = append(r.events, fmt.Sprintf("running %s<-%s", target, source))
	return nil
}

func (r *fakePatchRepo) Finish(_ context.Context, _, _, target, source string, status types.BundlePatchStatus, reason string, patchSize, fullDownloadSize *int64) error {
	sizes := "no sizes"
	if patchSize != nil && fullDownloadSize != nil {
		sizes = "with sizes"
	}
	r.events = append(r.events, fmt.Sprintf("finish %s<-%s %s %q %s", target, source, status, reason, sizes))
	return nil
}

func (r *fakePatchRepo) ListByTarget(context.Context, string, string, string) ([]types.BundlePatch, error) {
	return nil, nil
}

type patchJobHarness struct {
	*rolloutTestHarness
	service *BsDiffService
	patches *fakePatchRepo
}

// newPatchJobHarness wires the fakes to the real local bucket, so the job
// reads blobs, runs bsdiff and stores the patch exactly as in production.
func newPatchJobHarness(t *testing.T) *patchJobHarness {
	t.Helper()
	t.Setenv("DB_URL", "postgres://bsdiff-job-tests")
	t.Setenv("BUNDLE_DIFFING", "true")
	t.Setenv("BUNDLE_DIFFING_MAX_BUNDLE_SIZE_MB", "")
	t.Setenv("BUNDLE_DIFFING_PATCH_MAX_RATIO", "")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", t.TempDir())
	t.Setenv("BASE_URL", "http://localhost:3000")
	t.Setenv("JWT_SECRET", "test_jwt_secret")
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)

	h := newRolloutTestHarness(t)
	patches := &fakePatchRepo{}
	return &patchJobHarness{
		rolloutTestHarness: h,
		service:            NewBSDiffService(bucket.GetBucket(), nil, h.updateService, h.updateRepo, patches),
		patches:            patches,
	}
}

// seedBundle publishes an update whose launch asset is bundle: the row, its
// asset mapping and the blob in cas/. A nil bundle seeds a pre-CAS update
// that has no mapping.
func (h *patchJobHarness) seedBundle(t *testing.T, branch string, id int64, updateUUID string, bundle []byte) {
	t.Helper()
	update := h.seed(seedRow{branch: branch, rtv: "1", platform: "ios", id: id, checked: true, uuid: updateUUID})
	if bundle == nil {
		return
	}
	hash := blobHash(bundle)
	require.NoError(t, h.updateRepo.StoreUpdateAssetMapping(context.Background(), update, &types.UpdateAssetMapping{
		LaunchAsset: types.ShapedAsset{Hash: hash, Key: "bundle", FileExtension: ".hbc"},
	}))
	require.NoError(t, bucket.GetBucket().PutBlob(context.Background(), h.appId, hash, bytes.NewReader(bundle)))
}

func blobHash(data []byte) string {
	digest := sha256.Sum256(data)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// randomBundle is incompressible, so its gzipped size is its size and a
// small edit yields a patch far below the ratio.
func randomBundle(size int) []byte {
	data := make([]byte, size)
	rand.New(rand.NewSource(42)).Read(data)
	return data
}

func editedBundle(bundle []byte) []byte {
	edited := append([]byte(nil), bundle...)
	copy(edited[len(edited)/2:], []byte("a small change in the middle of the bundle"))
	return edited
}

func (h *patchJobHarness) run(t *testing.T, target, source string, attempt, maxAttempts int) error {
	t.Helper()
	job := &river.Job[bsDiffComputeArgs]{
		JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: maxAttempts},
		Args: bsDiffComputeArgs{
			AppId:            h.appId,
			Branch:           "main",
			TargetUpdateUUID: target,
			SourceUpdateUUID: source,
			TargetUpdateId:   "target",
			SourceUpdateId:   "source",
		},
	}
	return h.service.runPatchJob(context.Background(), job)
}

func (h *patchJobHarness) storedPatch(t *testing.T, target, source string) []byte {
	t.Helper()
	file, err := bucket.GetBucket().GetBSDiff(context.Background(), h.appId, "main", target, source)
	require.NoError(t, err)
	if file == nil {
		return nil
	}
	defer file.Reader.Close()
	body, err := io.ReadAll(file.Reader)
	require.NoError(t, err)
	return body
}

func TestPatchJobStoresAPatchThatRebuildsTheTarget(t *testing.T) {
	h := newPatchJobHarness(t)
	sourceBundle := randomBundle(64 * 1024)
	targetBundle := editedBundle(sourceBundle)
	h.seedBundle(t, "main", 100, patchTestCurrentUUID, sourceBundle)
	h.seedBundle(t, "main", 200, patchTestRequestedUUID, targetBundle)

	require.NoError(t, h.run(t, patchTestRequestedUUID, patchTestCurrentUUID, 1, 3))

	patch := h.storedPatch(t, patchTestRequestedUUID, patchTestCurrentUUID)
	require.NotNil(t, patch, "the patch must be in the bucket under target/source")
	rebuilt, err := bsdiff.Patch(sourceBundle, patch)
	require.NoError(t, err)
	assert.Equal(t, targetBundle, rebuilt)
	assert.Less(t, len(patch), len(targetBundle)/10)
	assert.Equal(t, []string{
		"running target<-source",
		`finish target<-source stored "" with sizes`,
	}, h.patches.events)
}

func TestPatchJobSkipsWithoutStoringAnything(t *testing.T) {
	bundle := randomBundle(64 * 1024)
	cases := []struct {
		name   string
		env    map[string]string
		seed   func(h *patchJobHarness)
		reason string
		sizes  string
	}{
		{
			name: "identical bundles",
			seed: func(h *patchJobHarness) {
				h.seedBundle(t, "main", 100, patchTestCurrentUUID, bundle)
				h.seedBundle(t, "main", 200, patchTestRequestedUUID, bundle)
			},
			reason: types.BundlePatchReasonIdenticalBundles,
			sizes:  "no sizes",
		},
		{
			name: "patch above the ratio",
			env:  map[string]string{"BUNDLE_DIFFING_PATCH_MAX_RATIO": "0.0001"},
			seed: func(h *patchJobHarness) {
				h.seedBundle(t, "main", 100, patchTestCurrentUUID, bundle)
				h.seedBundle(t, "main", 200, patchTestRequestedUUID, editedBundle(bundle))
			},
			reason: types.BundlePatchReasonNotWorth,
			sizes:  "with sizes",
		},
		{
			name: "target published before the CAS",
			seed: func(h *patchJobHarness) {
				h.seedBundle(t, "main", 100, patchTestCurrentUUID, bundle)
				h.seedBundle(t, "main", 200, patchTestRequestedUUID, nil)
			},
			reason: types.BundlePatchReasonLegacyUpdate,
			sizes:  "no sizes",
		},
		{
			name: "source published before the CAS",
			seed: func(h *patchJobHarness) {
				h.seedBundle(t, "main", 100, patchTestCurrentUUID, nil)
				h.seedBundle(t, "main", 200, patchTestRequestedUUID, editedBundle(bundle))
			},
			reason: types.BundlePatchReasonLegacyUpdate,
			sizes:  "no sizes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPatchJobHarness(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			tc.seed(h)

			require.NoError(t, h.run(t, patchTestRequestedUUID, patchTestCurrentUUID, 1, 3))

			assert.Nil(t, h.storedPatch(t, patchTestRequestedUUID, patchTestCurrentUUID))
			assert.Equal(t, []string{
				"running target<-source",
				fmt.Sprintf("finish target<-source skipped %q %s", tc.reason, tc.sizes),
			}, h.patches.events)
		})
	}
}

// Conditions a retry cannot fix cancel the job, so River does not retry it.
func TestPatchJobCancelsOnPermanentConditions(t *testing.T) {
	bundle := randomBundle(64 * 1024)
	cases := []struct {
		name   string
		env    map[string]string
		seed   func(h *patchJobHarness)
		reason string
	}{
		{
			name: "target update unknown",
			seed: func(h *patchJobHarness) {
				h.seedBundle(t, "main", 100, patchTestCurrentUUID, bundle)
			},
			reason: types.BundlePatchReasonUpdateNotFound,
		},
		{
			name: "updates on different branches",
			seed: func(h *patchJobHarness) {
				h.seedBundle(t, "internal", 100, patchTestCurrentUUID, bundle)
				h.seedBundle(t, "main", 200, patchTestRequestedUUID, editedBundle(bundle))
			},
			reason: types.BundlePatchReasonDifferentBranch,
		},
		{
			name: "bundle blob missing from the bucket",
			seed: func(h *patchJobHarness) {
				h.seedBundle(t, "main", 100, patchTestCurrentUUID, bundle)
				h.seedBundle(t, "main", 200, patchTestRequestedUUID, editedBundle(bundle))
				require.NoError(t, h.updateRepo.StoreUpdateAssetMapping(context.Background(),
					types.Update{AppId: h.appId, Branch: "main", RuntimeVersion: "1", UpdateId: "200"},
					&types.UpdateAssetMapping{LaunchAsset: types.ShapedAsset{Hash: blobHash([]byte("never uploaded"))}}))
			},
			reason: types.BundlePatchReasonBlobMissing,
		},
		{
			name: "bundle above the size limit",
			env:  map[string]string{"BUNDLE_DIFFING_MAX_BUNDLE_SIZE_MB": "1"},
			seed: func(h *patchJobHarness) {
				h.seedBundle(t, "main", 100, patchTestCurrentUUID, bundle)
				h.seedBundle(t, "main", 200, patchTestRequestedUUID, randomBundle(1<<20+1))
			},
			reason: types.BundlePatchReasonBundleTooLarge,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPatchJobHarness(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			tc.seed(h)

			err := h.run(t, patchTestRequestedUUID, patchTestCurrentUUID, 1, 3)

			var cancel *river.JobCancelError
			require.True(t, errors.As(err, &cancel), "expected a job cancel, got %v", err)
			assert.Contains(t, err.Error(), tc.reason)
			assert.Nil(t, h.storedPatch(t, patchTestRequestedUUID, patchTestCurrentUUID))
			require.Len(t, h.patches.events, 2)
			assert.Equal(t, "running target<-source", h.patches.events[0])
			assert.Contains(t, h.patches.events[1], "finish target<-source cancelled")
			assert.Contains(t, h.patches.events[1], tc.reason)
		})
	}
}
