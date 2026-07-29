package test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"xprem/internal/types"
)

func createRollbackRequest(projectRoot, branch, runtimeVersion, headerKey, headerValue, platform, commitHash string) (*httptest.ResponseRecorder, *mux.Router, *mux.Route, *http.Request) {
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	var q string
	if commitHash != "" {
		q = fmt.Sprintf("http://localhost:3000/rollback/%s?runtimeVersion=%s&platform=%s&commitHash=%s", branch, runtimeVersion, platform, commitHash)
	} else {
		q = fmt.Sprintf("http://localhost:3000/rollback/%s?runtimeVersion=%s&platform=%s", branch, runtimeVersion, platform)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r = mux.SetURLVars(r, map[string]string{"APP_ID": "test-app-id", "BRANCH": branch})
	r.Header.Set(headerKey, headerValue)
	return w, mux.NewRouter(), nil, r
}

func TestToRollbackWithBadBearer(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	w, _, _, r := createRollbackRequest(projectRoot, "DO_NOT_USE", "1", "Authorization", "Bearer expo_bad_token", "ios", "hash")
	testContainer().RollbackHandler.HandleRollback(w, r)
	assert.Equal(t, 401, w.Code, "Expected status code 401")
	assert.Equal(t, "Error validating auth\n", w.Body.String(), "Expected error message")
}

func TestGoodRollback(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	w, _, _, r := createRollbackRequest(projectRoot, "DO_NOT_USE", "1", "Authorization", "Bearer expo_test_token", "ios", "hash")
	testContainer().RollbackHandler.HandleRollback(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200")
	type Response struct {
		Branch         string `json:"branch"`
		RuntimeVersion string `json:"runtimeVersion"`
		UpdateId       string `json:"updateId"`
		CreatedAt      int64  `json:"createdAt"`
	}

	var body Response
	err = json.Unmarshal(w.Body.Bytes(), &body)
	require.NoError(t, err)

	assert.NotEmpty(t, body.UpdateId, "Expected non-empty updateId")
	assert.NotEmpty(t, body.RuntimeVersion, "Expected non-empty runtimeVersion")
	assert.NotEmpty(t, body.Branch, "Expected non-empty branch")
	assert.NotEmpty(t, body.CreatedAt, "Expected non-empty createdAt")
	lastUpdate, err := testLatestUpdate("test-app-id", "DO_NOT_USE", "1", "ios")
	if err != nil {
		t.Fatalf("Error getting latest update: %v", err)
	}
	assert.Equal(t, body.UpdateId, lastUpdate.UpdateId, "Expected updateId to match the latest update")
	updateType, err := testUpdateType(*lastUpdate)
	if err != nil {
		t.Fatalf("Error getting update type: %v", err)
	}
	assert.Equal(t, updateType, types.Rollback, "Expected update type to be rollback")
}

func TestGoodRollbackWithoutCommitHash(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	w, _, _, r := createRollbackRequest(projectRoot, "DO_NOT_USE", "1", "Authorization", "Bearer expo_test_token", "ios", "")
	testContainer().RollbackHandler.HandleRollback(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200")
	type Response struct {
		Branch         string `json:"branch"`
		RuntimeVersion string `json:"runtimeVersion"`
		UpdateId       string `json:"updateId"`
		CreatedAt      int64  `json:"createdAt"`
	}

	var body Response
	err = json.Unmarshal(w.Body.Bytes(), &body)
	require.NoError(t, err)

	assert.NotEmpty(t, body.UpdateId, "Expected non-empty updateId")
	assert.NotEmpty(t, body.RuntimeVersion, "Expected non-empty runtimeVersion")
	assert.NotEmpty(t, body.Branch, "Expected non-empty branch")
	assert.NotEmpty(t, body.CreatedAt, "Expected non-empty createdAt")
	lastUpdate, err := testLatestUpdate("test-app-id", "DO_NOT_USE", "1", "ios")
	if err != nil {
		t.Fatalf("Error getting latest update: %v", err)
	}
	assert.Equal(t, body.UpdateId, lastUpdate.UpdateId, "Expected updateId to match the latest update")
	updateType, err := testUpdateType(*lastUpdate)
	if err != nil {
		t.Fatalf("Error getting update type: %v", err)
	}
	assert.Equal(t, updateType, types.Rollback, "Expected update type to be rollback")
}

// TestRollbackDoesNotPoisonLatestUpdateCache is a regression test for the
// race in MarkUpdateAsChecked where cache.Delete used to run before the
// .check file was written.
//
// The race: between the cache invalidation and the .check write there was
// a wide window (StoreUpdateUUIDInMetadata does slow bucket I/O). A
// concurrent /manifest request in that window would miss the cache, scan
// the bucket, see the new update on disk WITHOUT a .check file, filter it
// out via IsUpdateValid, fall back to the previous update, and re-cache
// it under the lastUpdate key for the full 1800s TTL — serving a stale
// manifest for up to 30 minutes after the publish/rollback.
//
// The fix reorders MarkUpdateAsChecked to write .check before deleting
// the cache, so the bad intermediate state (new-update-without-check +
// empty-cache) is never visible to concurrent readers.
//
// This test is self-contained: it writes its own "stale" update into a
// dedicated branch before hammering readers while a rollback fires on
// that same branch. It does not depend on any shared fixture state, so
// it cannot be broken by test ordering or leaked PreWarm goroutines from
// previous tests.
func TestRollbackDoesNotPoisonLatestUpdateCache(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	const appId = "test-app-id"
	const branchName = "DO_NOT_USE" // GlobalAfterEach already cleans this branch under ./updates
	const runtimeVersion = "1"
	const platform = "android"
	const staleUpdateId = "1700000000000"

	// Use the same LOCAL_BUCKET_BASE_PATH that createRollbackRequest uses so
	// the bucket instance we build here is the same one the rollback handler
	// will use. Otherwise the singleton gets built at ./test/test-updates
	// first and the rollback silently writes to a different tree.
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))

	// Plant a "previous update" for the cache to latch onto. Without this,
	// a poisoned cache would simply have nothing to point at and the race
	// wouldn't produce a visible symptom. The update is registered on the
	// same branch/rv/platform that the rollback will target.
	stalePath := filepath.Join(projectRoot, "./updates/test-app-id", branchName, runtimeVersion, staleUpdateId)
	require.NoError(t, os.MkdirAll(stalePath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stalePath, "update-metadata.json"),
		[]byte(`{"platform":"android","commitHash":"stale","updateUUID":"00000000-0000-0000-0000-000000000001"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stalePath, ".check"), []byte(".check"), 0o644))

	// Read through UpdateService, which is what owns the lastUpdate cache that
	// MarkUpdateAsChecked invalidates. Reading the bucket store directly (as
	// testLatestUpdate does) is a pure scan and would never touch the cache
	// this test exists to protect.
	updateService := testUpdateService()
	ctx := context.Background()

	// Warm the lastUpdate cache with the planted update so a poisoned cache
	// can actually point at a stale value.
	warmed, err := updateService.GetLatestUpdate(ctx, appId, branchName, runtimeVersion, platform)
	require.NoError(t, err)
	require.NotNil(t, warmed, "fixture setup: planted update not picked up — check LOCAL_BUCKET_BASE_PATH wiring")
	require.Equal(t, staleUpdateId, warmed.UpdateId, "fixture setup: expected cache warmed with planted update")

	// Hammer the read path from many goroutines. The old ordering would
	// have given one of these readers a chance to re-cache the fixture
	// mid-rollback. Keep going past the rollback handler's return so any
	// poisoned value has to survive to the final assertion.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var reads int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = updateService.GetLatestUpdate(ctx, appId, branchName, runtimeVersion, platform)
					atomic.AddInt64(&reads, 1)
				}
			}
		}()
	}

	// Fire the rollback while readers are hammering.
	w, _, _, r := createRollbackRequest(projectRoot, branchName, runtimeVersion, "Authorization", "Bearer expo_test_token", platform, "hash")
	testContainer().RollbackHandler.HandleRollback(w, r)
	require.Equal(t, http.StatusOK, w.Code, "rollback handler failed: %s", w.Body.String())

	close(stop)
	wg.Wait()

	// Definitive assertion: after the rollback, the latest update must be
	// the rollback itself, not the planted stale update. A failure here
	// means a concurrent reader poisoned the cache with the stale entry.
	latest, err := updateService.GetLatestUpdate(ctx, appId, branchName, runtimeVersion, platform)
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.NotEqual(t, staleUpdateId, latest.UpdateId, "lastUpdate cache poisoned with stale update after %d reads", reads)
	latestType, err := testUpdateType(*latest)
	require.NoError(t, err)
	assert.Equal(t, types.Rollback, latestType, "expected latest update to be a rollback")
}
