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
	"sync"
	"testing"

	"xprem/internal/bucket"
	cache2 "xprem/internal/cache"
	"xprem/internal/crypto"
	"xprem/internal/types"
	"xprem/internal/update"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
)

func createUploadRequest(t *testing.T, projectRoot, branch, runtimeVersion, sampleUpdatePath, headerKey, headerValue string, platform types.Platform) (*httptest.ResponseRecorder, *mux.Router, *mux.Route, *http.Request) {
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	q := fmt.Sprintf("http://localhost:3000/test-app-id/requestUploadUrl/%s?runtimeVersion=%s&platform=%s&commitHash=abc123", branch, runtimeVersion, platform)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", q, nil)
	r.Header.Set(headerKey, headerValue)
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath)
	uploadRequestsInputJSON, err := json.Marshal(uploadRequestsInput)
	if err != nil {
		t.Fatalf("Error marshalling uploadRequestsInput: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(uploadRequestsInputJSON))
	return w, mux.NewRouter(), nil, r
}

func performUpload(t *testing.T, projectRoot, branch, runtimeVersion, sampleUpdatePath string, platform types.Platform) string {
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	requestURL := fmt.Sprintf("http://localhost:3000/test-app-id/requestUploadUrl/%s?runtimeVersion=%s&platform=%s&commitHash=abc123", branch, runtimeVersion, platform)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", requestURL, nil)
	r.Header.Set("Authorization", "Bearer expo_test_token")
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath)
	uploadRequestsInputJSON, err := json.Marshal(uploadRequestsInput)
	if err != nil {
		t.Fatalf("Error marshalling uploadRequestsInput: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(uploadRequestsInputJSON))
	serveThroughRouter(w, r)
	if w.Code != 200 {
		t.Fatalf("RequestUploadUrlHandler returned status %d instead of 200", w.Code)
	}
	var responseBody struct {
		UpdateId       int64                      `json:"updateId"`
		UploadRequests []bucket.FileUploadRequest `json:"uploadRequests"`
	}
	if err := json.NewDecoder(w.Body).Decode(&responseBody); err != nil {
		t.Fatalf("Error decoding response body: %v", err)
	}
	updateId := fmt.Sprintf("%d", responseBody.UpdateId)
	fileUploadRequests := responseBody.UploadRequests
	ws := make([]*httptest.ResponseRecorder, len(fileUploadRequests))
	errs := make(chan error, len(fileUploadRequests))
	var wg sync.WaitGroup
	for i, uploadRequest := range fileUploadRequests {
		wg.Add(1)
		go func(index int, req bucket.FileUploadRequest) {
			defer wg.Done()
			ws[index] = httptest.NewRecorder()
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)
			localFilePath := filepath.Join(sampleUpdatePath, req.FilePath)
			fileBuffer, err := os.Open(localFilePath)
			if err != nil {
				errs <- fmt.Errorf("Error opening file %s: %w", localFilePath, err)
				return
			}
			defer fileBuffer.Close()
			part, err := writer.CreateFormFile(req.FileName, req.FileName)
			if err != nil {
				errs <- fmt.Errorf("Error creating multipart form file: %w", err)
				return
			}
			if _, err = io.Copy(part, fileBuffer); err != nil {
				errs <- fmt.Errorf("Error copying file to multipart part: %w", err)
				return
			}
			if err = writer.Close(); err != nil {
				errs <- fmt.Errorf("Error closing multipart writer: %w", err)
				return
			}
			parsedUrl, err := url.Parse(req.RequestUploadUrl)
			if err != nil {
				errs <- fmt.Errorf("Error parsing URL %s: %w", req.RequestUploadUrl, err)
				return
			}
			token := parsedUrl.Query().Get("token")
			uploadReq := httptest.NewRequest("PUT", "/test-app-id/uploadLocalFile?token="+token, body)
			uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
			uploadReq.Header.Set("Authorization", "Bearer expo_test_token")
			serveThroughRouter(ws[index], uploadReq)
			if ws[index].Code != 200 {
				errs <- fmt.Errorf("File upload for %s returned status %d", req.FileName, ws[index].Code)
			}
		}(i, uploadRequest)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Error during file uploads: %v", err)
	}
	for i, recorder := range ws {
		if recorder.Code != 200 {
			t.Fatalf("A file upload returned status %d instead of 200", recorder.Code)
		}
		expectedFilePath := filepath.Join(projectRoot, "updates", "test-app-id", "cas", fileUploadRequests[i].Hash)
		if _, err := os.Open(expectedFilePath); err != nil {
			t.Fatalf("Error opening uploaded file %s: %v", expectedFilePath, err)
		}
	}
	metadataPath := filepath.Join(projectRoot, "updates", "test-app-id", branch, runtimeVersion, updateId, "update-metadata.json")
	metadataContent, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("Error opening update-metadata.json file at %s: %v", metadataPath, err)
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal(metadataContent, &metadata); err != nil {
		t.Fatalf("Error unmarshalling update-metadata.json: %v", err)
	}
	if metadata["platform"] != string(platform) || metadata["commitHash"] != "abc123" {
		t.Fatalf("Metadata values not as expected, got: %v", metadata)
	}
	return updateId
}

func markUpdateAsUploaded(t *testing.T, branch, runtimeVersion, updateId string, platform types.Platform) *httptest.ResponseRecorder {
	markURL := fmt.Sprintf("http://localhost:3000/test-app-id/markUpdateAsUploaded/%s?platform=%s&runtimeVersion=%s&updateId=%s", branch, platform, runtimeVersion, updateId)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", markURL, nil)
	r.Header.Set("Authorization", "Bearer expo_test_token")
	serveThroughRouter(w, r)
	return w
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
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath)
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
	body, err := json.Marshal(map[string]any{"files": []map[string]string{{"name": "metadata.json"}}})
	if err != nil {
		t.Fatalf("Error marshalling body: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	serveThroughRouter(w, r)
	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "invalid hash")
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
		"files": []map[string]string{{"name": "metadata.json", "hash": "not-a-hash"}},
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
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath)
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
		expectedFile := filepath.Join(projectRoot, "updates", "test-app-id", "cas", uploadRequests[i].Hash)
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
	assert.NotNil(t, lastUpdate, "Expected non-nil")
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
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath)
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
	expectedValue := "{\"appId\":\"test-app-id\",\"branch\":\"branch-1\",\"runtimeVersion\":\"1\",\"updateId\":\"1674170951\",\"createdAt\":1674170951000000}"
	assert.Equal(t, expectedValue, value, "Expected a specific cache value")
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath)
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
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath)
	uploadRequestsInputJSON, err := json.Marshal(uploadRequestsInput)
	if err != nil {
		t.Fatalf("Error marshalling uploadRequestsInput: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(uploadRequestsInputJSON))
	serveThroughRouter(w, r)
	assert.Equal(t, 401, w.Code, "Expected status code 401")
	assert.Equal(t, "Error validating auth\n", w.Body.String(), "Expected error message")
}

func TestIdenticalUpload(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	sampleUpdatePath := filepath.Join(projectRoot, "test", "test-updates", "test-app-id", "branch-4", "1", "1674170952")
	branch := "DO_NOT_USE"
	runtimeVersion := "1"
	updateId1 := performUpload(t, projectRoot, branch, runtimeVersion, sampleUpdatePath, "ios")
	w := markUpdateAsUploaded(t, branch, runtimeVersion, updateId1, "ios")
	if w.Code != 200 {
		t.Fatalf("First mark as uploaded failed with status %d", w.Code)
	}
	updateId2 := performUpload(t, projectRoot, branch, runtimeVersion, sampleUpdatePath, "ios")
	w2 := markUpdateAsUploaded(t, branch, runtimeVersion, updateId2, "ios")
	if w2.Code == 200 {
		t.Fatalf("Second mark as uploaded should have failed (non-200), got %d", w2.Code)
	}
	lastUpdate, err := testLatestUpdate("test-app-id", branch, runtimeVersion, "ios")
	if err != nil {
		t.Fatalf("Error getting latest update: %v", err)
	}
	assert.NotNil(t, lastUpdate, "Expected non-nil")
	assert.Equal(t, updateId1, lastUpdate.UpdateId, "Expected update ID to match")
}

func TestDifferentUpload(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	sampleUpdatePath := filepath.Join(projectRoot, "test", "test-updates", "test-app-id", "branch-4", "1", "1674170952")
	branch := "DO_NOT_USE"
	runtimeVersion := "1"
	updateId1 := performUpload(t, projectRoot, branch, runtimeVersion, sampleUpdatePath, "android")
	w := markUpdateAsUploaded(t, branch, runtimeVersion, updateId1, "android")
	if w.Code != 200 {
		t.Fatalf("First mark as uploaded failed with status %d", w.Code)
	}
	sampleOtherUpdatePath := filepath.Join(projectRoot, "test", "test-updates", "test-app-id", "branch-4", "1", "1674170951")
	updateId2 := performUpload(t, projectRoot, branch, runtimeVersion, sampleOtherUpdatePath, "android")
	w2 := markUpdateAsUploaded(t, branch, runtimeVersion, updateId2, "android")
	assert.Equal(t, 200, w2.Code, "Expected status code 200")
	lastUpdate, err := testLatestUpdate("test-app-id", branch, runtimeVersion, "android")
	if err != nil {
		t.Fatalf("Error getting latest update: %v", err)
	}
	assert.NotNil(t, lastUpdate, "Expected non-nil")
	assert.Equal(t, updateId2, lastUpdate.UpdateId, "Expected update ID to match")
}
