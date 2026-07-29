package test

import (
	"encoding/json"
	"fmt"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"xprem/internal/types"
	"xprem/internal/update"
)

func createRepublishRequest(branch, runtimeVersion, headerKey, headerValue, platform, commitHash, updateId string) (*httptest.ResponseRecorder, *mux.Router, *mux.Route, *http.Request) {
	var q string
	if commitHash != "" {
		q = fmt.Sprintf("http://localhost:3000/republish/%s?runtimeVersion=%s&platform=%s&updateId=%s&commitHash=%s", branch, runtimeVersion, platform, updateId, commitHash)
	} else {
		q = fmt.Sprintf("http://localhost:3000/republish/%s?runtimeVersion=%s&updateId=%s&platform=%s", branch, runtimeVersion, updateId, platform)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r = mux.SetURLVars(r, map[string]string{"APP_ID": "test-app-id", "BRANCH": branch})
	r.Header.Set(headerKey, headerValue)
	return w, mux.NewRouter(), nil, r
}

func TestToRepublishRollbackWithBadBearer(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	w, _, _, r := createRepublishRequest("branch-2", "1", "Authorization", "Bearer expo_bad_token", "ios", "hash", "1737455526")
	testContainer().RepublishHandler.HandleRepublish(w, r)
	assert.Equal(t, 401, w.Code, "Expected status code 401")
	assert.Equal(t, "Error validating auth\n", w.Body.String(), "Expected error message")
}

func copyDir(src string, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, os.ModePerm)
		}

		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		err = os.MkdirAll(filepath.Dir(dstPath), os.ModePerm)
		if err != nil {
			return err
		}

		dstFile, err := os.Create(dstPath)
		if err != nil {
			return err
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, srcFile)
		return err
	})
}

func TestGoodRepublish(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates", "DO_NOT_USE"))
	src := filepath.Join(projectRoot, "test", "test-updates")
	dst := filepath.Join(projectRoot, "updates", "DO_NOT_USE")

	err = copyDir(src, dst)
	if err != nil {
		panic(err)
	}
	w, _, _, r := createRepublishRequest("branch-2", "1", "Authorization", "Bearer expo_test_token", "ios", "hash", "1737455526")
	testContainer().RepublishHandler.HandleRepublish(w, r)
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
	lastUpdate, err := testLatestUpdate("test-app-id", "branch-2", "1", "ios")
	if err != nil {
		t.Fatalf("Error getting latest update: %v", err)
	}
	assert.Equal(t, body.UpdateId, lastUpdate.UpdateId, "Expected updateId to match the latest update")
	updateType, err := testUpdateType(*lastUpdate)
	if err != nil {
		t.Fatalf("Error getting update type: %v", err)
	}
	assert.Equal(t, updateType, types.NormalUpdate, "Expected update type to be normal")

	previousUpdate, err := testUpdate("test-app-id", "branch-2", "1", "1737455526")
	if err != nil {
		t.Fatalf("Error getting previous update: %v", err)
	}
	if previousUpdate == nil {
		t.Fatalf("Expected previous update to exist")
	}
	previousMetadata, err := update.GetMetadata(*previousUpdate)
	if err != nil {
		t.Fatalf("Error getting previous update metadata: %v", err)
	}
	lastMetadata, err := update.GetMetadata(*lastUpdate)
	if err != nil {
		t.Fatalf("Error getting last update metadata: %v", err)
	}
	previousFingerprint := previousMetadata.Fingerprint
	previousId := previousUpdate.UpdateId

	assert.Equal(t, previousFingerprint, lastMetadata.Fingerprint, "Expected fingerprint to match")
	assert.NotEqual(t, previousId, lastUpdate.UpdateId, "Expected updateId to be different")
}

func TestGoodRepublishWithoutCommitHash(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates", "DO_NOT_USE"))
	src := filepath.Join(projectRoot, "test", "test-updates")
	dst := filepath.Join(projectRoot, "updates", "DO_NOT_USE")

	err = copyDir(src, dst)
	if err != nil {
		panic(err)
	}
	w, _, _, r := createRepublishRequest("branch-2", "1", "Authorization", "Bearer expo_test_token", "ios", "", "1737455526")
	testContainer().RepublishHandler.HandleRepublish(w, r)
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
	lastUpdate, err := testLatestUpdate("test-app-id", "branch-2", "1", "ios")
	if err != nil {
		t.Fatalf("Error getting latest update: %v", err)
	}
	assert.Equal(t, body.UpdateId, lastUpdate.UpdateId, "Expected updateId to match the latest update")
	updateType, err := testUpdateType(*lastUpdate)
	if err != nil {
		t.Fatalf("Error getting update type: %v", err)
	}
	assert.Equal(t, updateType, types.NormalUpdate, "Expected update type to be normal")

	previousUpdate, err := testUpdate("test-app-id", "branch-2", "1", "1737455526")
	if err != nil {
		t.Fatalf("Error getting previous update: %v", err)
	}
	if previousUpdate == nil {
		t.Fatalf("Expected previous update to exist")
	}
	previousMetadata, err := update.GetMetadata(*previousUpdate)
	if err != nil {
		t.Fatalf("Error getting previous update metadata: %v", err)
	}
	lastMetadata, err := update.GetMetadata(*lastUpdate)
	if err != nil {
		t.Fatalf("Error getting last update metadata: %v", err)
	}
	previousFingerprint := previousMetadata.Fingerprint
	previousId := previousUpdate.UpdateId

	assert.Equal(t, previousFingerprint, lastMetadata.Fingerprint, "Expected fingerprint to match")
	assert.NotEqual(t, previousId, lastUpdate.UpdateId, "Expected updateId to be different")
}

func TestRepublishOnBadPlatform(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates", "DO_NOT_USE"))
	src := filepath.Join(projectRoot, "test", "test-updates")
	dst := filepath.Join(projectRoot, "updates", "DO_NOT_USE")

	err = copyDir(src, dst)
	if err != nil {
		panic(err)
	}
	w, _, _, r := createRepublishRequest("branch-2", "1", "Authorization", "Bearer expo_test_token", "android", "", "1737455526")
	testContainer().RepublishHandler.HandleRepublish(w, r)
	assert.Equal(t, 400, w.Code, "Expected status code 400")
	assert.Equal(t, "Update platform mismatch\n", w.Body.String(), "Expected error message")
}

func TestRepublishInvalidUpdate(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates", "DO_NOT_USE"))
	src := filepath.Join(projectRoot, "test", "test-updates")
	dst := filepath.Join(projectRoot, "updates", "DO_NOT_USE")

	err = copyDir(src, dst)
	if err != nil {
		panic(err)
	}
	// rm the file projectDir/updates/DO_NOT_USE/test-app-id/branch-2/1/1737455526/.check
	err = os.Remove(filepath.Join(dst, "test-app-id", "branch-2", "1", "1737455526", ".check"))
	if err != nil {
		panic(err)
	}
	w, _, _, r := createRepublishRequest("branch-2", "1", "Authorization", "Bearer expo_test_token", "ios", "", "1737455526")
	testContainer().RepublishHandler.HandleRepublish(w, r)
	assert.Equal(t, 400, w.Code, "Expected status code 400")
	assert.Equal(t, "Update is not valid\n", w.Body.String(), "Expected error message")
}

func TestRepublishWithBadUpdate(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates", "DO_NOT_USE"))
	src := filepath.Join(projectRoot, "test", "test-updates")
	dst := filepath.Join(projectRoot, "updates", "DO_NOT_USE")

	err = copyDir(src, dst)
	if err != nil {
		panic(err)
	}
	w, _, _, r := createRepublishRequest("branch-2", "1", "Authorization", "Bearer expo_test_token", "ios", "", "BAD_ONE")
	testContainer().RepublishHandler.HandleRepublish(w, r)
	assert.Equal(t, 400, w.Code, "Expected status code 400")
	assert.Equal(t, "Error getting update\n", w.Body.String(), "Expected error message")
}

func TestToRepublishARollback(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates", "DO_NOT_USE"))
	src := filepath.Join(projectRoot, "test", "test-updates")
	dst := filepath.Join(projectRoot, "updates", "DO_NOT_USE")

	err = copyDir(src, dst)
	if err != nil {
		panic(err)
	}
	w, _, _, r := createRepublishRequest("branch-2", "1", "Authorization", "Bearer expo_test_token", "ios", "", "1666629141")
	testContainer().RepublishHandler.HandleRepublish(w, r)
	assert.Equal(t, 400, w.Code, "Expected status code 400")
	assert.Equal(t, "Update type is not normal update\n", w.Body.String(), "Expected error message")
}
