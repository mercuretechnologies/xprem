package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
	"xprem/internal/bucket"
	"xprem/internal/crypto"
	"xprem/internal/jobs"
	"xprem/internal/providers/expo"
	"xprem/internal/store"
	"xprem/internal/types"

	"github.com/jarcoal/httpmock"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// importFakeUpdateImporter fakes the one UpdateRepository method the history
// import calls; the embedded interface satisfies the rest and panics if the
// import ever grows an unexpected repository call.
type importFakeUpdateImporter struct {
	UpdateRepository
	mu        sync.Mutex
	rows      []store.ImportUpdateParams
	err       error
	duplicate bool
	// exists makes every timeline slot look occupied, so imports skip before
	// writing any file.
	exists bool
	// hook runs before each insert, letting a test hold the job mid-run.
	hook func()
}

func (f *importFakeUpdateImporter) UpdateExists(context.Context, string, string, int64) (bool, error) {
	return f.exists, nil
}

func (f *importFakeUpdateImporter) ImportUpdate(_ context.Context, params store.ImportUpdateParams) (bool, error) {
	if f.hook != nil {
		f.hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	f.rows = append(f.rows, params)
	return !f.duplicate, nil
}

func (f *importFakeUpdateImporter) importedRows() []store.ImportUpdateParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.ImportUpdateParams(nil), f.rows...)
}

// importFakeBucket fakes the two bucket methods the history import calls; the
// embedded interface satisfies the rest and panics if the import ever grows
// an unexpected bucket call.
type importFakeBucket struct {
	bucket.Bucket
	mu             sync.Mutex
	files          map[string][]byte
	deletedFolders []string
}

func (f *importFakeBucket) UploadFileIntoUpdate(update types.Update, fileName string, file io.Reader) error {
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[update.UpdateId+"/"+fileName] = data
	return nil
}

func (f *importFakeBucket) DeleteUpdateFolder(_ string, _ string, _ string, updateId string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedFolders = append(f.deletedFolders, updateId)
	return nil
}

func (f *importFakeBucket) file(updateId string, name string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[updateId+"/"+name]
	return data, ok
}

const (
	historyBundleURL       = "https://assets.eascdn.net/bundle"
	historyAssetURL        = "https://assets.eascdn.net/asset"
	historyPermalinkURL    = "https://u.expo.dev/update/manifest"
	historyIOSPermalink    = historyPermalinkURL + "/ios"
	historyAndroidPermalik = historyPermalinkURL + "/android"
	historyBundleAuth      = "Bearer bundle-token"
	historyAssetAuth       = "Bearer asset-token"
)

var (
	historyBundleBytes = []byte("history-bundle-bytes")
	historyAssetBytes  = []byte("history-asset-bytes")
)

func historyAssetHash(t *testing.T, data []byte) string {
	t.Helper()
	hash, err := crypto.CreateHash(data, "sha256", "base64")
	require.NoError(t, err)
	return crypto.GetBase64URLEncoding(hash)
}

func historyServedManifest(t *testing.T, bundleHash string, assetHash string) string {
	t.Helper()
	manifest, err := json.Marshal(map[string]interface{}{
		"id":             "11111111-1111-1111-1111-111111111111",
		"createdAt":      "2026-01-03T10:20:30.400Z",
		"runtimeVersion": "1.0.0",
		"launchAsset": map[string]interface{}{
			"key":           "bundlekey123",
			"hash":          bundleHash,
			"contentType":   "application/javascript",
			"fileExtension": ".bundle",
			"url":           historyBundleURL,
		},
		"assets": []map[string]interface{}{
			{
				"key":           "assetkey456",
				"hash":          assetHash,
				"contentType":   "image/png",
				"fileExtension": ".png",
				"url":           historyAssetURL,
			},
		},
		"metadata": map[string]interface{}{},
		"extra": map[string]interface{}{
			"expoClient": map[string]interface{}{"name": "My Imported App"},
		},
	})
	require.NoError(t, err)
	return string(manifest)
}

// historyMultipartResponder wraps a manifest the way EAS permalinks serve it:
// a multipart/mixed body with a "manifest" part and an "extensions" part
// carrying the per-asset authorization headers.
func historyMultipartResponder(t *testing.T, manifestJSON string) httpmock.Responder {
	t.Helper()
	extensions, err := json.Marshal(map[string]interface{}{
		"assetRequestHeaders": map[string]interface{}{
			"bundlekey123": map[string]string{"authorization": historyBundleAuth},
			"assetkey456":  map[string]string{"authorization": historyAssetAuth},
		},
	})
	require.NoError(t, err)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, payload := range map[string]string{"manifest": manifestJSON, "extensions": string(extensions)} {
		part, err := writer.CreateFormField(name)
		require.NoError(t, err)
		_, err = part.Write([]byte(payload))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	contentType := "multipart/mixed; boundary=" + writer.Boundary()
	payload := body.Bytes()
	return func(*http.Request) (*http.Response, error) {
		resp := httpmock.NewBytesResponse(http.StatusOK, payload)
		resp.Header.Set("Content-Type", contentType)
		return resp, nil
	}
}

// historyProtectedAssetResponder answers like the EAS CDN: 403 unless the
// request carries the authorization header announced in the extensions part.
func historyProtectedAssetResponder(auth string, data []byte) httpmock.Responder {
	return func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != auth {
			return httpmock.NewStringResponse(http.StatusForbidden, "Unauthorized asset request"), nil
		}
		return httpmock.NewBytesResponse(http.StatusOK, data), nil
	}
}

// mockExpoUpdateGroups serves three update groups, newest first: a two-platform
// normal group, a code-signed one, and a rollback.
func mockExpoUpdateGroups(t *testing.T) {
	t.Helper()
	manifest := historyServedManifest(t, historyAssetHash(t, historyBundleBytes), historyAssetHash(t, historyAssetBytes))
	httpmock.RegisterResponder("GET", historyIOSPermalink, historyMultipartResponder(t, manifest))
	httpmock.RegisterResponder("GET", historyAndroidPermalik, historyMultipartResponder(t, manifest))
	makeUpdate := func(id, group, platform, createdAt string, rollback bool, signed bool) map[string]interface{} {
		update := map[string]interface{}{
			"id":                   id,
			"group":                group,
			"message":              "publish message",
			"createdAt":            createdAt,
			"platform":             platform,
			"manifestPermalink":    historyPermalinkURL + "/" + platform,
			"isRollBackToEmbedded": rollback,
			"gitCommitHash":        "abc1234",
			"codeSigningInfo":      nil,
			"runtime":              map[string]interface{}{"id": "r1", "version": "1.0.0"},
			"branch":               map[string]interface{}{"id": "b1", "name": "production"},
		}
		if signed {
			update["codeSigningInfo"] = map[string]interface{}{"keyid": "main"}
		}
		return update
	}
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("operationName") != "FetchExpoUpdateGroups" {
				return httpmock.NewStringResponse(http.StatusNotFound, "unknown operation"), nil
			}
			return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
				"data": map[string]interface{}{
					"app": map[string]interface{}{
						"byId": map[string]interface{}{
							"id": importExpoAppID,
							"updateGroups": []interface{}{
								[]interface{}{
									makeUpdate("11111111-1111-1111-1111-111111111111", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "ios", "2026-01-03T10:20:30.400Z", false, false),
									makeUpdate("22222222-2222-2222-2222-222222222222", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "android", "2026-01-03T10:20:30.400Z", false, false),
								},
								[]interface{}{
									makeUpdate("44444444-4444-4444-4444-444444444444", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "android", "2026-01-02T00:00:02.000Z", false, true),
								},
								[]interface{}{
									makeUpdate("33333333-3333-3333-3333-333333333333", "cccccccc-cccc-cccc-cccc-cccccccccccc", "ios", "2026-01-01T00:00:01.000Z", true, false),
								},
							},
						},
					},
				},
			})
		})
	httpmock.RegisterResponder("GET", historyBundleURL, historyProtectedAssetResponder(historyBundleAuth, historyBundleBytes))
	httpmock.RegisterResponder("GET", historyAssetURL, historyProtectedAssetResponder(historyAssetAuth, historyAssetBytes))
}

func historyUpdateIdFor(t *testing.T, createdAt string, platform types.Platform) int64 {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, createdAt)
	require.NoError(t, err)
	return parsed.UnixMilli()*10 + historyPlatformDigit(platform)
}

// fetchHistoryGroups pulls the mocked update groups the way StartHistoryImport
// snapshots them into the job's args.
func fetchHistoryGroups(t *testing.T) [][]expo.HistoryUpdate {
	t.Helper()
	token := "token"
	groups, err := expo.FetchUpdateGroups(context.Background(), types.Auth{Token: &token}, importExpoAppID, 10)
	require.NoError(t, err)
	return groups
}

func TestCopyHistoryCopiesUpdates(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	mockExpoUpdateGroups(t)

	branchRepo := &importFakeBranchRepo{}
	importer := &importFakeUpdateImporter{}
	historyBucket := &importFakeBucket{}
	service := historyImportService(t, branchRepo, importer, historyBucket)

	tracker := jobs.NewTracker(1)
	require.NoError(t, service.copyHistory(context.Background(), importExpoAppID, tracker, fetchHistoryGroups(t)))

	progress := tracker.Output()
	assert.Equal(t, 4, progress.Processed)
	assert.Equal(t, 3, progress.Succeeded)
	require.Len(t, progress.Warnings, 1)
	assert.Contains(t, progress.Warnings[0], "code signing")

	rows := importer.importedRows()
	require.Len(t, rows, 3)

	// Oldest group lands first: the rollback, then the two-platform publish.
	rollback := rows[0]
	assert.Equal(t, types.Rollback, rollback.UpdateType)
	assert.Nil(t, rollback.UpdateUUID)
	assert.Equal(t, historyUpdateIdFor(t, "2026-01-01T00:00:01.000Z", types.PlatformIOS), rollback.UpdateId)
	assert.Equal(t, rollback.CreatedAt, rollback.CheckedAt)

	iosRow := rows[1]
	assert.Equal(t, types.NormalUpdate, iosRow.UpdateType)
	assert.Equal(t, types.PlatformIOS, iosRow.Platform)
	assert.Equal(t, historyUpdateIdFor(t, "2026-01-03T10:20:30.400Z", types.PlatformIOS), iosRow.UpdateId)
	require.NotNil(t, iosRow.UpdateUUID)
	assert.Equal(t, "11111111-1111-1111-1111-111111111111", *iosRow.UpdateUUID)
	require.NotNil(t, iosRow.PublishGroup)
	assert.Equal(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", *iosRow.PublishGroup)
	assert.Equal(t, "abc1234", iosRow.CommitHash)
	assert.Equal(t, "publish message", iosRow.Message)
	assert.Equal(t, "production", iosRow.BranchName)
	assert.Equal(t, "1.0.0", iosRow.RuntimeVersion)

	androidRow := rows[2]
	assert.Equal(t, types.PlatformAndroid, androidRow.Platform)
	assert.Equal(t, historyUpdateIdFor(t, "2026-01-03T10:20:30.400Z", types.PlatformAndroid), androidRow.UpdateId)

	assert.Contains(t, branchRepo.upserted, "production@1.0.0")

	iosUpdateId := strconv.FormatInt(iosRow.UpdateId, 10)
	metadataBytes, ok := historyBucket.file(iosUpdateId, "metadata.json")
	require.True(t, ok)
	var metadata types.MetadataObject
	require.NoError(t, json.Unmarshal(metadataBytes, &metadata))
	assert.Equal(t, "bundles/ios-bundlekey123.bundle", metadata.FileMetadata.IOS.Bundle)
	require.Len(t, metadata.FileMetadata.IOS.Assets, 1)
	assert.Equal(t, "assets/assetkey456", metadata.FileMetadata.IOS.Assets[0].Path)
	assert.Equal(t, "png", metadata.FileMetadata.IOS.Assets[0].Ext)
	assert.Empty(t, metadata.FileMetadata.Android.Bundle)

	bundleBytes, ok := historyBucket.file(iosUpdateId, "bundles/ios-bundlekey123.bundle")
	require.True(t, ok)
	assert.Equal(t, historyBundleBytes, bundleBytes)
	assetBytes, ok := historyBucket.file(iosUpdateId, "assets/assetkey456")
	require.True(t, ok)
	assert.Equal(t, historyAssetBytes, assetBytes)

	storedBytes, ok := historyBucket.file(iosUpdateId, "update-metadata.json")
	require.True(t, ok)
	var stored types.UpdateStoredMetadata
	require.NoError(t, json.Unmarshal(storedBytes, &stored))
	assert.Equal(t, "11111111-1111-1111-1111-111111111111", stored.UpdateUUID)
	assert.Equal(t, types.PlatformIOS, stored.Platform)

	expoConfigBytes, ok := historyBucket.file(iosUpdateId, "expoConfig.json")
	require.True(t, ok)
	assert.JSONEq(t, `{"name":"My Imported App"}`, string(expoConfigBytes))

	// The rollback is a row without files.
	_, ok = historyBucket.file(strconv.FormatInt(rollback.UpdateId, 10), "metadata.json")
	assert.False(t, ok)

	// The shared asset is downloaded once for the whole job, the launch
	// asset once per platform update.
	callCounts := httpmock.GetCallCountInfo()
	assert.Equal(t, 1, callCounts["GET "+historyAssetURL])
	assert.Equal(t, 2, callCounts["GET "+historyBundleURL])
}

func TestCopyHistoryStopsOnCanceledContext(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	mockExpoUpdateGroups(t)

	// River delivers a cancel by canceling the worker's context; the first
	// insert triggers it here, mid-run.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	importer := &importFakeUpdateImporter{hook: cancel}
	service := historyImportService(t, &importFakeBranchRepo{}, importer, &importFakeBucket{})

	tracker := jobs.NewTracker(1)
	err := service.copyHistory(ctx, importExpoAppID, tracker, fetchHistoryGroups(t))

	require.ErrorIs(t, err, context.Canceled)
	// The update underway when the cancel landed is not reported: the job
	// stops without counting work interrupted halfway.
	require.Len(t, importer.importedRows(), 1)
	assert.Equal(t, 0, tracker.Output().Processed)
}

func TestCopyHistoryFailsWhenStoreIsDown(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	mockExpoUpdateGroups(t)

	importer := &importFakeUpdateImporter{err: errors.New("connection lost")}
	service := historyImportService(t, &importFakeBranchRepo{}, importer, &importFakeBucket{})

	err := service.copyHistory(context.Background(), importExpoAppID, jobs.NewTracker(1), fetchHistoryGroups(t))
	require.ErrorContains(t, err, "connection lost")
}

func TestCancelHistoryImportUnknownJob(t *testing.T) {
	service := historyImportService(t, &importFakeBranchRepo{}, &importFakeUpdateImporter{}, &importFakeBucket{})
	require.ErrorIs(t, service.CancelHistoryJob(context.Background(), "424242"), ErrHistoryJobNotFound)
}

func TestStartHistoryImportRequiresControlPlane(t *testing.T) {
	service := historyImportService(t, &importFakeBranchRepo{}, &importFakeUpdateImporter{}, &importFakeBucket{})
	t.Setenv("DB_URL", "")

	_, err := service.StartHistoryImport(context.Background(), expoAuth("token"), importExpoAppID, 10)
	require.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
}

func TestStartHistoryImportValidatesLimit(t *testing.T) {
	service := historyImportService(t, &importFakeBranchRepo{}, &importFakeUpdateImporter{}, &importFakeBucket{})

	_, err := service.StartHistoryImport(context.Background(), expoAuth("token"), importExpoAppID, 0)
	require.Error(t, err)
	_, err = service.StartHistoryImport(context.Background(), expoAuth("token"), importExpoAppID, MaxHistoryImportGroups+1)
	require.Error(t, err)
}

func TestHistoryJobStatusMapping(t *testing.T) {
	discarded := &rivertype.JobRow{
		State:       rivertype.JobStateDiscarded,
		EncodedArgs: []byte(`{"appId":"` + importExpoAppID + `","total":4}`),
		Metadata:    []byte(`{"output":{"processed":3,"succeeded":2,"warnings":["update x (ios): skipped"]}}`),
		Errors:      []rivertype.AttemptError{{Error: "transient"}, {Error: "connection lost"}},
	}
	status := historyJobStatus(discarded)
	assert.Equal(t, jobs.StateFailed, status.State)
	assert.Equal(t, 4, status.Total)
	assert.Equal(t, 3, status.Processed)
	assert.Equal(t, 2, status.Imported)
	assert.Equal(t, []string{"update x (ios): skipped"}, status.Skipped)
	assert.Equal(t, "connection lost", status.Error)
	assert.False(t, status.CancelRequested)

	// A cancel on a running job lands in its metadata; the dashboard shows
	// the button as pressed on every replica.
	running := &rivertype.JobRow{
		State:       rivertype.JobStateRunning,
		EncodedArgs: []byte(`{"total":4}`),
		Metadata:    []byte(`{"cancel_attempted_at":"2026-08-29T10:00:00Z"}`),
	}
	status = historyJobStatus(running)
	assert.Equal(t, jobs.StateRunning, status.State)
	assert.True(t, status.CancelRequested)
	assert.Empty(t, status.Error)

	// A job waiting for its automatic retry reads as still running, and its
	// transient error stays out of sight.
	retryable := &rivertype.JobRow{
		State:       rivertype.JobStateRetryable,
		EncodedArgs: []byte(`{"total":4}`),
		Metadata:    []byte(`{}`),
		Errors:      []rivertype.AttemptError{{Error: "transient"}},
	}
	status = historyJobStatus(retryable)
	assert.Equal(t, jobs.StateRunning, status.State)
	assert.Empty(t, status.Error)

	completed := &rivertype.JobRow{
		State:       rivertype.JobStateCompleted,
		EncodedArgs: []byte(`{"total":4}`),
		Metadata:    []byte(`{"output":{"processed":4,"succeeded":4}}`),
	}
	status = historyJobStatus(completed)
	assert.Equal(t, jobs.StateDone, status.State)
	assert.Equal(t, 4, status.Imported)

	cancelled := &rivertype.JobRow{
		State:       rivertype.JobStateCancelled,
		EncodedArgs: []byte(`{"total":4}`),
		Metadata:    []byte(`{}`),
	}
	assert.Equal(t, jobs.StateCanceled, historyJobStatus(cancelled).State)
}

func TestImportHistoryUpdateSkipsHashMismatch(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	httpmock.RegisterResponder("GET", historyBundleURL, httpmock.NewBytesResponder(http.StatusOK, historyBundleBytes))
	httpmock.RegisterResponder("GET", historyAssetURL, httpmock.NewBytesResponder(http.StatusOK, []byte("tampered")))
	// Serves the manifest as bare JSON to cover the non-multipart fallback.
	manifest := historyServedManifest(t, historyAssetHash(t, historyBundleBytes), historyAssetHash(t, historyAssetBytes))
	httpmock.RegisterResponder("GET", historyIOSPermalink, func(*http.Request) (*http.Response, error) {
		resp := httpmock.NewStringResponse(http.StatusOK, manifest)
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	})

	historyBucket := &importFakeBucket{}
	service := historyImportService(t, &importFakeBranchRepo{}, &importFakeUpdateImporter{}, historyBucket)

	skipReason, err := service.importHistoryUpdate(context.Background(), importExpoAppID, expoHistoryUpdateFixture(historyIOSPermalink), map[branchRuntime]bool{}, newHistoryAssetCache())

	require.NoError(t, err)
	assert.Contains(t, skipReason, "does not match its manifest hash")
	require.Len(t, historyBucket.deletedFolders, 1)
}

func TestImportHistoryUpdateSkipsUnsupportedPlatform(t *testing.T) {
	service := historyImportService(t, &importFakeBranchRepo{}, &importFakeUpdateImporter{}, &importFakeBucket{})

	update := expoHistoryUpdateFixture("")
	update.Platform = "web"
	skipReason, err := service.importHistoryUpdate(context.Background(), importExpoAppID, update, map[branchRuntime]bool{}, newHistoryAssetCache())

	require.NoError(t, err)
	assert.Contains(t, skipReason, "not supported")
}

// EAS branch names land in bucket paths: one that CreateBranch would refuse
// must not slip in through the history import either.
func TestImportHistoryUpdateSkipsInvalidBranchName(t *testing.T) {
	branchRepo := &importFakeBranchRepo{}
	importer := &importFakeUpdateImporter{}
	service := historyImportService(t, branchRepo, importer, &importFakeBucket{})

	update := expoHistoryUpdateFixture("")
	update.BranchName = "bad*branch"
	skipReason, err := service.importHistoryUpdate(context.Background(), importExpoAppID, update, map[branchRuntime]bool{}, newHistoryAssetCache())

	require.NoError(t, err)
	assert.Contains(t, skipReason, "bad*branch")
	assert.Empty(t, branchRepo.upserted)
	assert.Empty(t, importer.importedRows())
}

func TestImportHistoryUpdateSkipsInvalidRuntimeVersion(t *testing.T) {
	branchRepo := &importFakeBranchRepo{}
	service := historyImportService(t, branchRepo, &importFakeUpdateImporter{}, &importFakeBucket{})

	update := expoHistoryUpdateFixture("")
	update.RuntimeVersion = "../escape"
	skipReason, err := service.importHistoryUpdate(context.Background(), importExpoAppID, update, map[branchRuntime]bool{}, newHistoryAssetCache())

	require.NoError(t, err)
	assert.Contains(t, skipReason, "runtime version")
	assert.Empty(t, branchRepo.upserted)
}

// An occupied timeline slot skips the update before any bucket write, so a
// re-import never overwrites the files of an update that already exists.
func TestImportHistoryUpdateSkipsOccupiedSlotBeforeWriting(t *testing.T) {
	branchRepo := &importFakeBranchRepo{}
	importer := &importFakeUpdateImporter{exists: true}
	historyBucket := &importFakeBucket{}
	service := historyImportService(t, branchRepo, importer, historyBucket)

	skipReason, err := service.importHistoryUpdate(context.Background(), importExpoAppID, expoHistoryUpdateFixture(historyIOSPermalink), map[branchRuntime]bool{}, newHistoryAssetCache())

	require.NoError(t, err)
	assert.Contains(t, skipReason, "already exists")
	assert.Empty(t, historyBucket.files)
	assert.Empty(t, branchRepo.upserted)
	assert.Empty(t, importer.importedRows())
}

// A failed insert must not leave the freshly written files behind as orphans:
// the retry starts from an empty slot.
func TestImportHistoryUpdateCleansUpFolderWhenInsertFails(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	mockExpoUpdateGroups(t)

	importer := &importFakeUpdateImporter{err: errors.New("insert failed")}
	historyBucket := &importFakeBucket{}
	service := historyImportService(t, &importFakeBranchRepo{}, importer, historyBucket)

	_, err := service.importHistoryUpdate(context.Background(), importExpoAppID, expoHistoryUpdateFixture(historyIOSPermalink), map[branchRuntime]bool{}, newHistoryAssetCache())

	require.ErrorContains(t, err, "insert failed")
	// The files were written, then swept with the folder.
	assert.NotEmpty(t, historyBucket.files)
	require.Len(t, historyBucket.deletedFolders, 1)
	expectedUpdateId := historyUpdateIdFor(t, "2026-01-03T10:20:30.400Z", types.PlatformIOS)
	assert.Equal(t, strconv.FormatInt(expectedUpdateId, 10), historyBucket.deletedFolders[0])
}

func expoHistoryUpdateFixture(permalink string) expo.HistoryUpdate {
	return expo.HistoryUpdate{
		Id:                "11111111-1111-1111-1111-111111111111",
		Group:             "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		BranchName:        "production",
		RuntimeVersion:    "1.0.0",
		Platform:          "ios",
		Message:           "publish message",
		GitCommitHash:     "abc1234",
		CreatedAt:         "2026-01-03T10:20:30.400Z",
		ManifestPermalink: permalink,
	}
}
