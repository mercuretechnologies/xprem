package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"xprem/internal/bucket"
	cache2 "xprem/internal/cache"
	"xprem/internal/crypto"
	"xprem/internal/services"
	"xprem/internal/types"
	"xprem/internal/update"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createUploadRequest(t *testing.T, projectRoot, branch, runtimeVersion, sampleUpdatePath, headerKey, headerValue string, platform types.Platform) (*httptest.ResponseRecorder, *mux.Router, *mux.Route, *http.Request) {
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	q := fmt.Sprintf("http://localhost:3000/test-app-id/requestUploadUrl/%s?runtimeVersion=%s&platform=%s&commitHash=abc123", branch, runtimeVersion, platform)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r.Header.Set(headerKey, headerValue)
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath, platform)
	uploadRequestsInputJSON, err := json.Marshal(uploadRequestsInput)
	if err != nil {
		t.Fatalf("Error marshalling uploadRequestsInput: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(uploadRequestsInputJSON))
	return w, mux.NewRouter(), nil, r
}

func TestRequestUploadUrlWithoutBearer(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	sampleUpdatePath := filepath.Join(projectRoot, "/test/test-updates/test-app-id/branch-1/1/1674170951")
	w, _, _, r := createUploadRequest(t, projectRoot, "DO_NOT_USE", "1", sampleUpdatePath, "Authorization", "Bearer expo_alternative_token", "ios")
	serveThroughRouter(w, r)
	assert.Equal(t, 401, w.Code, "Expected status code 401")
	assert.Equal(t, "Error validating auth\n", w.Body.String(), "Expected error message")
}

func TestRequestUploadUrlWithBadBearer(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	sampleUpdatePath := filepath.Join(projectRoot, "/test/test-updates/test-app-id/branch-1/1/1674170951")
	w, _, _, r := createUploadRequest(t, projectRoot, "DO_NOT_USE", "1", sampleUpdatePath, "Authorization", "Bearer expo_bad_token", "ios")
	serveThroughRouter(w, r)
	assert.Equal(t, 401, w.Code, "Expected status code 401")
	assert.Equal(t, "Error validating auth\n", w.Body.String(), "Expected error message")
}

func TestRequestUploadUrlWithoutRuntimeVersion(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	q := "http://localhost:3000/test-app-id/requestUploadUrl/DO_NOT_USE"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r.Header.Set("Authorization", "Bearer expo_test_token")
	sampleUpdatePath := filepath.Join(projectRoot, "/test/test-updates/test-app-id/branch-1/1/1674170951")
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath, "android")
	uploadRequestsInputJSON, err := json.Marshal(uploadRequestsInput)
	if err != nil {
		t.Fatalf("Error marshalling uploadRequestsInput: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(uploadRequestsInputJSON))
	serveThroughRouter(w, r)
	assert.Equal(t, 400, w.Code, "Expected status code 400")
	assert.Equal(t, "No runtime version provided\n", w.Body.String(), "Expected error message")
}

func TestRequestUploadUrlWithBadRequestBody(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	q := "http://localhost:3000/test-app-id/requestUploadUrl/DO_NOT_USE?runtimeVersion=1"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r.Header.Set("Authorization", "Bearer expo_test_token")
	uploadRequestsInputJSON, err := json.Marshal(map[string]string{"id": "4"})
	if err != nil {
		t.Fatalf("Error marshalling uploadRequestsInput: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(uploadRequestsInputJSON))
	serveThroughRouter(w, r)
	assert.Equal(t, 400, w.Code, "Expected status code 400")
	assert.Equal(t, "No file names provided\n", w.Body.String(), "Expected error message")
}

func TestRequestUploadUrlRejectsMissingHash(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	q := "http://localhost:3000/test-app-id/requestUploadUrl/DO_NOT_USE?runtimeVersion=1"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r.Header.Set("Authorization", "Bearer expo_test_token")
	body, err := json.Marshal(map[string]any{
		"files": []map[string]string{{"path": "metadata.json", "role": "config"}},
	})
	if err != nil {
		t.Fatalf("Error marshalling body: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	serveThroughRouter(w, r)
	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "invalid hash")
}

func TestRequestUploadUrlRejectsMalformedManifestKey(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	sample := filepath.Join(projectRoot, "test", "test-updates", "test-app-id", "branch-4", "1", "1674170952")
	body := ComputeUploadRequestsInput(sample, "android")
	for i := range body.Files {
		if body.Files[i].Role == services.FileRoleLaunch {
			// A sha256 base64url where an md5 hex belongs: the shape the CLI
			// would send if the two digests were ever swapped.
			body.Files[i].Key = body.Files[i].Hash
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Error marshalling body: %v", err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "http://localhost:3000/test-app-id/requestUploadUrl/DO_NOT_USE?runtimeVersion=1&platform=android", bytes.NewReader(payload))
	r.Header.Set("Authorization", "Bearer expo_test_token")
	serveThroughRouter(w, r)
	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "invalid key")
}

func TestRequestUploadUrlRejectsMalformedHash(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	q := "http://localhost:3000/test-app-id/requestUploadUrl/DO_NOT_USE?runtimeVersion=1"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r.Header.Set("Authorization", "Bearer expo_test_token")
	body, err := json.Marshal(map[string]any{
		"files": []map[string]string{{"path": "metadata.json", "hash": "not-a-hash", "role": "config"}},
	})
	if err != nil {
		t.Fatalf("Error marshalling body: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	serveThroughRouter(w, r)
	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "invalid hash")
}

func TestRequestUploadUrlWithBadFilenamesType(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	q := "http://localhost:3000/test-app-id/requestUploadUrl/DO_NOT_USE?runtimeVersion=1"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r.Header.Set("Authorization", "Bearer expo_test_token")
	uploadRequestsInputJSON, err := json.Marshal(map[string]int{"files": 1})
	if err != nil {
		t.Fatalf("Error marshalling uploadRequestsInput: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(uploadRequestsInputJSON))
	serveThroughRouter(w, r)
	assert.Equal(t, 400, w.Code, "Expected status code 400")
	assert.Equal(t, "Invalid JSON body\n", w.Body.String(), "Expected error message")
}

func TestRequestUploadUrlWithSampleUpdate(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	q := "http://localhost:3000/test-app-id/requestUploadUrl/DO_NOT_USE?runtimeVersion=1&platform=android&commitHash=abc123"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r.Header.Set("Authorization", "Bearer expo_test_token")
	sampleUpdatePath := filepath.Join(projectRoot, "/test/test-updates/test-app-id/branch-4/1/1674170952")
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath, "android")
	uploadRequestsInputJSON, err := json.Marshal(uploadRequestsInput)
	if err != nil {
		t.Fatalf("Error marshalling uploadRequestsInput: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(uploadRequestsInputJSON))
	serveThroughRouter(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200")
	var responseBody struct {
		UpdateId       int64                      `json:"updateId"`
		UploadRequests []bucket.FileUploadRequest `json:"uploadRequests"`
	}
	if err := json.NewDecoder(w.Body).Decode(&responseBody); err != nil {
		assert.Fail(t, "Expected valid JSON response")
	}
	uploadRequests := responseBody.UploadRequests
	assert.Len(t, uploadRequests, 4, "Expected 4 file upload requests")
	updateIdHeader := w.Header().Get("expo-update-id")
	assert.NotEmpty(t, updateIdHeader, "Expected non-empty update ID")
	for _, req := range uploadRequests {
		parsedUrl, err := url.Parse(req.RequestUploadUrl)
		assert.Nil(t, err, "Expected valid URL")
		assert.Equal(t, "http", parsedUrl.Scheme, "Expected HTTP scheme")
		assert.Equal(t, "localhost:3000", parsedUrl.Host, "Expected localhost:3000 host")
		assert.Equal(t, "/test-app-id/uploadLocalFile", parsedUrl.Path, "Expected /{appId}/uploadLocalFile path")
		token := parsedUrl.Query().Get("token")
		assert.NotEmpty(t, token, "Expected non-empty token")
		claims := jwt.MapClaims{}
		decoded, err := crypto.DecodeAndExtractJWTToken("test_jwt_secret", token, claims)
		assert.Nil(t, err, "Expected valid JWT token")
		if !decoded.Valid {
			assert.Fail(t, "Expected valid JWT token")
		}
		filePath, ok := claims["filePath"].(string)
		assert.True(t, ok, "Expected filePath to be a string")
		assert.NotEmpty(t, filePath, "Expected non-empty file path")
		sub, ok := claims["sub"].(string)
		assert.True(t, ok, "Expected sub to be a string")
		assert.Equal(t, "test_username", sub, "Expected test_username sub")
	}
	var (
		ws   = make([]*httptest.ResponseRecorder, len(uploadRequests))
		errs = make(chan error, len(uploadRequests))
		wg   sync.WaitGroup
	)
	for i, req := range uploadRequests {
		wg.Add(1)
		go func(index int, uploadReq bucket.FileUploadRequest) {
			defer wg.Done()
			ws[index] = httptest.NewRecorder()
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)
			filePath := filepath.Join(projectRoot, "/test/test-updates/test-app-id/branch-4/1/1674170952", uploadReq.FilePath)
			fileBuffer, err := os.Open(filePath)
			if err != nil {
				errs <- err
				return
			}
			part, err := writer.CreateFormFile(uploadReq.FileName, uploadReq.FileName)
			if err != nil {
				errs <- err
				return
			}
			_, err = io.Copy(part, fileBuffer)
			if err != nil {
				errs <- err
				return
			}
			_ = writer.Close()
			parsedUrl, err := url.Parse(uploadReq.RequestUploadUrl)
			if err != nil {
				errs <- err
				return
			}
			token := parsedUrl.Query().Get("token")
			uploadFileReq := httptest.NewRequest("PUT", "/test-app-id/uploadLocalFile?token="+token, body)
			uploadFileReq.Header.Set("Content-Type", writer.FormDataContentType())
			uploadFileReq.Header.Set("Authorization", "Bearer expo_test_token")
			serveThroughRouter(ws[index], uploadFileReq)
			if ws[index].Code != 200 {
				errs <- fmt.Errorf("Upload failed with status %d", ws[index].Code)
			}
		}(i, req)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.Nil(t, err, "Expected no errors")
	}
	for i, rec := range ws {
		assert.Equal(t, 200, rec.Code, "Expected status code 200")
		// The launch asset and the assets are addressed by content; the config
		// files stay in the update folder, where the manifest reads them by name.
		expectedFile := filepath.Join(projectRoot, "updates", "test-app-id", "cas", uploadRequests[i].Hash)
		if path := uploadRequests[i].FilePath; path == "metadata.json" || path == "expoConfig.json" {
			expectedFile = filepath.Join(projectRoot, "updates", "test-app-id", "DO_NOT_USE", "1", updateIdHeader, path)
		}
		if _, err := os.Open(expectedFile); err != nil {
			assert.Nil(t, err, "Expected no errors when opening uploaded file")
		}
	}
	lastUpdate, err := testLatestUpdate("test-app-id", "DO_NOT_USE", "1", "android")
	if err != nil {
		t.Fatalf("Error getting latest update: %v", err)
	}
	assert.Nil(t, lastUpdate, "Expected nil")
	qMark := "http://localhost:3000/test-app-id/markUpdateAsUploaded/DO_NOT_USE?platform=android&runtimeVersion=1&updateId=" + updateIdHeader
	wMark := httptest.NewRecorder()
	rMark := httptest.NewRequest("POST", qMark, nil)
	rMark.Header.Set("Authorization", "Bearer expo_test_token")
	serveThroughRouter(wMark, rMark)
	assert.Equal(t, 200, wMark.Code, "Expected status code 200")
	lastUpdate, err = testLatestUpdate("test-app-id", "DO_NOT_USE", "1", "android")
	if err != nil {
		t.Fatalf("Error getting latest update: %v", err)
	}
	require.NotNil(t, lastUpdate, "Expected non-nil")
	assert.Equal(t, updateIdHeader, lastUpdate.UpdateId, "Expected update ID to match")
}

func TestRequestUploadUrlWithValidExpoSession(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	q := "http://localhost:3000/test-app-id/requestUploadUrl/DO_NOT_USE?runtimeVersion=1&platform=android&commitHash=abc123"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r.Header.Set("expo-session", "expo_test_session")
	sampleUpdatePath := filepath.Join(projectRoot, "/test/test-updates/test-app-id/branch-1/1/1674170951")
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath, "android")
	uploadRequestsInputJSON, err := json.Marshal(uploadRequestsInput)
	if err != nil {
		t.Fatalf("Error marshalling uploadRequestsInput: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(uploadRequestsInputJSON))
	serveThroughRouter(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200")
	assert.NotEmpty(t, w.Header().Get("expo-update-id"), "Expected non-empty update ID")
}

func TestShouldPreserveCacheOnUploadRequest(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "/test/test-updates"))
	mockWorkingExpoResponse("staging")
	qManifest := "http://localhost:3000/manifest"
	wManifest := httptest.NewRecorder()
	rManifest := httptest.NewRequest("GET", qManifest, nil)
	rManifest.Header.Add("expo-platform", "android")
	rManifest.Header.Add("expo-runtime-version", "1")
	rManifest.Header.Add("expo-protocol-version", "1")
	rManifest.Header.Add("expo-expect-signature", "true")
	rManifest.Header.Add("expo-channel-name", "staging")
	rManifest.Header.Add("expo-app-id", "test-app-id")
	testContainer().ExpoProtocolHandler.HandleManifest(wManifest, rManifest)
	assert.Equal(t, 200, wManifest.Code, "Expected status code 200")

	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	q := "http://localhost:3000/test-app-id/requestUploadUrl/branch-1?runtimeVersion=1&platform=android&commitHash=abc123"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r.Header.Set("expo-session", "expo_test_session")
	sampleUpdatePath := filepath.Join(projectRoot, "/test/test-updates/test-app-id/branch-1/1/1674170951")
	cache := cache2.GetCache()
	cacheKey := update.ComputeLastUpdateCacheKey("test-app-id", "branch-1", "1", "android")
	value := cache.Get(cacheKey)
	expectedValue := "{\"appId\":\"test-app-id\",\"branch\":\"branch-1\",\"runtimeVersion\":\"1\",\"updateId\":\"1674170951\",\"createdAt\":1674170951000000,\"updateUuid\":\"04b793a0-b6ab-fd4f-308c-b91d812adec2\"}"
	assert.Equal(t, expectedValue, value, "Expected a specific cache value")
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath, "android")
	uploadRequestsInputJSON, err := json.Marshal(uploadRequestsInput)
	if err != nil {
		t.Fatalf("Error marshalling uploadRequestsInput: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(uploadRequestsInputJSON))
	serveThroughRouter(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200")
	assert.NotEmpty(t, w.Header().Get("expo-update-id"), "Expected non-empty update ID")
	value = cache.Get(cacheKey)
	assert.Equal(t, expectedValue, value, "Expected cache to be preserved after RequestUploadUrl")
}

func TestRequestUploadUrlWithInvalidExpoSession(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	q := "http://localhost:3000/test-app-id/requestUploadUrl/DO_NOT_USE?runtimeVersion=1&platform=android&commitHash=abc123"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r.Header.Set("expo-session", "invalid_session_token")
	sampleUpdatePath := filepath.Join(projectRoot, "/test/test-updates/test-app-id/branch-1/1/1674170951")
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath, "android")
	uploadRequestsInputJSON, err := json.Marshal(uploadRequestsInput)
	if err != nil {
		t.Fatalf("Error marshalling uploadRequestsInput: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(uploadRequestsInputJSON))
	serveThroughRouter(w, r)
	assert.Equal(t, 401, w.Code, "Expected status code 401")
	assert.Equal(t, "Error validating auth\n", w.Body.String(), "Expected error message")
}

func postRequestUploadUrl(t *testing.T, branch, runtimeVersion string, platform types.Platform, sampleUpdatePath string) *httptest.ResponseRecorder {
	t.Helper()
	input, err := json.Marshal(ComputeUploadRequestsInput(sampleUpdatePath, platform))
	if err != nil {
		t.Fatalf("Error marshalling upload request: %v", err)
	}
	q := fmt.Sprintf("http://localhost:3000/test-app-id/requestUploadUrl/%s?runtimeVersion=%s&platform=%s&commitHash=abc123", branch, runtimeVersion, platform)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, bytes.NewReader(input))
	r.Header.Set("Authorization", "Bearer expo_test_token")
	serveThroughRouter(w, r)
	return w
}

// markUpdateChecked writes the sentinel the bucket store reads, standing in for
// markUpdateAsUploaded, which cannot run until the serve path reads from cas/.
func markUpdateChecked(t *testing.T, basePath, branch, runtimeVersion string, updateId int64) {
	t.Helper()
	sentinel := filepath.Join(basePath, "test-app-id", branch, runtimeVersion, strconv.FormatInt(updateId, 10), ".check")
	if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
		t.Fatalf("Error writing .check sentinel: %v", err)
	}
}

func updateIdFromResponse(t *testing.T, w *httptest.ResponseRecorder) int64 {
	t.Helper()
	var body struct {
		UpdateId int64 `json:"updateId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("Error decoding requestUploadUrl response: %v", err)
	}
	return body.UpdateId
}

// The stateless round trip: the asset mapping is written into
// update-metadata.json at publish and read back on the next publish, so a
// second identical run is refused without a database. Run over both export
// layouts, because nothing may infer a platform from a file name.
func TestStatelessIdenticalPublishIsRefused(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		path     []string
		platform types.Platform
	}{
		{"legacy bundles layout", []string{"branch-4", "1", "1674170952"}, "android"},
		{"expo static js layout", []string{"branch-2", "1", "1737455526"}, "ios"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			teardown := setup(t)
			defer teardown()
			mockExpoForRequestUploadUrlTest("staging")
			projectRoot, err := findProjectRoot()
			if err != nil {
				t.Fatalf("Error finding project root: %v", err)
			}
			basePath := filepath.Join(projectRoot, "updates")
			os.Setenv("LOCAL_BUCKET_BASE_PATH", basePath)
			sample := filepath.Join(append([]string{projectRoot, "test", "test-updates", "test-app-id"}, fixture.path...)...)

			first := postRequestUploadUrl(t, "DO_NOT_USE", "1", fixture.platform, sample)
			assert.Equal(t, http.StatusOK, first.Code)
			markUpdateChecked(t, basePath, "DO_NOT_USE", "1", updateIdFromResponse(t, first))

			second := postRequestUploadUrl(t, "DO_NOT_USE", "1", fixture.platform, sample)
			assert.Equal(t, http.StatusNotAcceptable, second.Code)
			assert.Contains(t, second.Body.String(), "no changes detected")
		})
	}
}

// A publish whose bundle differs from the checked latest goes through.
func TestStatelessDifferentPublishIsAccepted(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	basePath := filepath.Join(projectRoot, "updates")
	os.Setenv("LOCAL_BUCKET_BASE_PATH", basePath)
	fixtures := filepath.Join(projectRoot, "test", "test-updates", "test-app-id")

	first := postRequestUploadUrl(t, "DO_NOT_USE", "1", "android", filepath.Join(fixtures, "branch-4", "1", "1674170952"))
	assert.Equal(t, http.StatusOK, first.Code)
	markUpdateChecked(t, basePath, "DO_NOT_USE", "1", updateIdFromResponse(t, first))

	second := postRequestUploadUrl(t, "DO_NOT_USE", "1", "android", filepath.Join(fixtures, "branch-2", "1", "1737455526"))
	assert.Equal(t, http.StatusOK, second.Code)
}
