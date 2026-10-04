package services

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"xprem/internal/bucket"
	"xprem/internal/types"

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

type unavailableSourcemapStore struct {
	*bucket.SourcemapStore
	err error
}

func (s unavailableSourcemapStore) Exists(context.Context, string, string) (bool, error) {
	return false, s.err
}

type unavailableSourcemapMetadata struct {
	UpdateRepository
	err error
}

func (r unavailableSourcemapMetadata) GetUpdateSourcemapHash(context.Context, types.Update) (*string, error) {
	return nil, r.err
}

func TestProcessUploadedUpdateRetriesUnavailableSourcemapVerification(t *testing.T) {
	for _, mode := range []string{"storage timeout", "metadata timeout", "uploads disabled", "map missing"} {
		t.Run(mode, func(t *testing.T) {
			svc, h, store := newSourcemapTestHarness(t)
			ctx := context.Background()
			sourcemap := sourcemapUpload()
			params := publishParams(h, publishFiles(launchAssetPath))
			params.Sourcemap = &sourcemap
			resp, err := svc.RequestUploadURLs(ctx, params)
			require.NoError(t, err)
			update, err := h.updateRepo.GetUpdate(ctx, h.appId, "main", "1", strconv.FormatInt(resp.UpdateID, 10))
			require.NoError(t, err)
			stores := bucket.GetBucket()
			require.NoError(t, stores.UpdateStore.PutFile(ctx, *update, "metadata.json", strings.NewReader(`{"version":0,"bundler":"metro","fileMetadata":{"ios":{"bundle":"bundles/launch.hbc","assets":[]}}}`)))
			require.NoError(t, stores.UpdateStore.PutFile(ctx, *update, "expoConfig.json", strings.NewReader(`{"name":"healthy-upload"}`)))
			for _, file := range params.Files {
				if file.Role != FileRoleConfig {
					require.NoError(t, stores.BlobStore.Put(ctx, h.appId, file.Hash, strings.NewReader(file.Path)))
				}
			}
			if mode != "map missing" {
				require.NoError(t, store.Put(ctx, h.appId, sourcemap.Hash, strings.NewReader(sourcemap.Path)))
			}
			transient := errors.New("storage timeout")
			repository := svc.updateRepo
			switch mode {
			case "storage timeout":
				svc.SetSourcemapStore(unavailableSourcemapStore{store, transient})
			case "uploads disabled":
				svc.SetSourcemapStore(nil)
			case "metadata timeout":
				svc.updateRepo = unavailableSourcemapMetadata{repository, transient}
			}
			process := ProcessUpdateParams{AppID: h.appId, BranchName: "main", RuntimeVersion: "1", UpdateID: update.UpdateId, Platform: types.PlatformIOS}
			_, err = svc.ProcessUploadedUpdate(ctx, process)
			require.Error(t, err)
			metadata, readErr := stores.UpdateStore.GetFile(ctx, *update, "metadata.json")
			require.NoError(t, readErr)
			if mode == "map missing" {
				require.ErrorIs(t, err, ErrInvalidUpdate)
				require.Nil(t, metadata)
				return
			}
			require.ErrorIs(t, err, ErrSourcemapVerificationUnavailable)
			require.NotErrorIs(t, err, ErrInvalidUpdate)
			if mode == "storage timeout" || mode == "metadata timeout" {
				require.ErrorIs(t, err, transient)
			}
			require.NotNil(t, metadata, "temporary source map failures must preserve valid uploaded files")
			svc.SetSourcemapStore(store)
			svc.updateRepo = repository
			manifestID, err := svc.ProcessUploadedUpdate(ctx, process)
			require.NoError(t, err, "finalization must succeed without re-upload after recovery")
			require.NotEmpty(t, manifestID)
		})
	}
}
