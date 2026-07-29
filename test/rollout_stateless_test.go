package test

// Stateless-mode regressions for progressive rollouts: without a database control
// plane the rolloutPercentage publish parameter must be refused (or, at 100, behave as
// a plain publish), and the rollout-related client hints (EAS-Client-ID bucketing,
// Expo-Requested-Update-ID pinning) must leave /manifest and /assets responses exactly
// as they are today.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"xprem/internal/update"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildRolloutUploadRequest builds an authenticated requestUploadUrl request for the
// DO_NOT_USE branch with an optional rolloutPercentage query parameter.
func buildRolloutUploadRequest(t *testing.T, projectRoot string, rolloutPercentage string) *httptest.ResponseRecorder {
	t.Helper()
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	requestURL := "http://localhost:3000/requestUploadUrl/DO_NOT_USE?runtimeVersion=1&platform=ios&commitHash=abc123"
	if rolloutPercentage != "" {
		requestURL += "&rolloutPercentage=" + rolloutPercentage
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", requestURL, nil)
	r = mux.SetURLVars(r, map[string]string{"APP_ID": "test-app-id", "BRANCH": "DO_NOT_USE"})
	r.Header.Set("Authorization", "Bearer expo_test_token")
	sampleUpdatePath := filepath.Join(projectRoot, "/test/test-updates/test-app-id/branch-4/1/1674170952")
	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath)
	uploadRequestsInputJSON, err := json.Marshal(uploadRequestsInput)
	require.NoError(t, err)
	r.Body = io.NopCloser(bytes.NewReader(uploadRequestsInputJSON))
	testContainer().UploadHandler.RequestUploadUrlHandler(w, r)
	return w
}

func TestRequestUploadUrlWithRolloutPercentageRejectedInStatelessMode(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	w := buildRolloutUploadRequest(t, projectRoot, "20")
	assert.Equal(t, 400, w.Code, "Expected status code 400")
	assert.Equal(t, "Progressive rollouts require the database control plane\n", w.Body.String())
}

func TestRequestUploadUrlWithInvalidRolloutPercentage(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	for _, invalidPercentage := range []string{"0", "101", "-5", "abc", "20.5"} {
		t.Run(invalidPercentage, func(t *testing.T) {
			w := buildRolloutUploadRequest(t, projectRoot, invalidPercentage)
			assert.Equal(t, 400, w.Code, "Expected status code 400")
			assert.Equal(t, "Invalid rolloutPercentage: must be an integer between 1 and 99\n", w.Body.String())
		})
	}
}

func TestRequestUploadUrlWithRolloutPercentage100BehavesAsPlainPublish(t *testing.T) {
	// 100 means every device, i.e. a plain publish: it must pass in stateless mode and
	// must NOT be echoed back (the echo is the CLI's signal that a rollout started).
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	w := buildRolloutUploadRequest(t, projectRoot, "100")
	assert.Equal(t, 200, w.Code, "Expected status code 200")
	assert.NotEmpty(t, w.Header().Get("expo-update-id"), "Expected non-empty update ID")
	var responseBody map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&responseBody))
	assert.NotContains(t, responseBody, "rolloutPercentage", "a plain publish must not echo a rollout percentage")
	assert.Contains(t, responseBody, "uploadRequests")
}

func TestManifestIgnoresRolloutHeadersInStatelessMode(t *testing.T) {
	// The rollout decision inputs (EAS-Client-ID) and the asset pinning header
	// (Expo-Requested-Update-ID) must not change what a stateless server serves.
	teardown := setup(t)
	defer teardown()
	mockWorkingExpoResponse("staging")

	requestManifest := func(withRolloutHeaders bool) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "http://localhost:3000/manifest", nil)
		r.Header.Add("expo-platform", "android")
		r.Header.Add("expo-runtime-version", "1")
		r.Header.Add("expo-protocol-version", "1")
		r.Header.Add("expo-expect-signature", "true")
		r.Header.Add("expo-channel-name", "staging")
		r.Header.Add("expo-app-id", "test-app-id")
		if withRolloutHeaders {
			r.Header.Add("EAS-Client-ID", "d8a43b52-9b1b-4b6a-9e57-52a2ce271244")
			r.Header.Add("Expo-Requested-Update-ID", "04b793a0-b6ab-fd4f-308c-b91d812adec2")
		}
		testContainer().ExpoProtocolHandler.HandleManifest(w, r)
		return w
	}

	baselineResponse := requestManifest(false)
	withHeadersResponse := requestManifest(true)
	assert.Equal(t, 200, baselineResponse.Code)
	assert.Equal(t, 200, withHeadersResponse.Code)

	baselineParts, err := ParseMultipartMixedResponse(baselineResponse.Header().Get("Content-Type"), baselineResponse.Body.Bytes())
	require.NoError(t, err)
	withHeadersParts, err := ParseMultipartMixedResponse(withHeadersResponse.Header().Get("Content-Type"), withHeadersResponse.Body.Bytes())
	require.NoError(t, err)
	require.Len(t, baselineParts, 1)
	require.Len(t, withHeadersParts, 1)
	assert.True(t, IsMultipartPartWithName(withHeadersParts[0], "manifest"), "Expected a part with name 'manifest'")
	assert.Equal(t, baselineParts[0].Body, withHeadersParts[0].Body, "the manifest body must be byte-identical with and without rollout headers")
}

func TestAssetsIgnoreRolloutHeadersInStatelessMode(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockWorkingExpoResponse("staging")
	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	assetURL, err := update.BuildFinalManifestAssetUrlURL("http://localhost:3000", "bundles/android-82adadb1fb6e489d04ad95fd79670deb.js", "1", "android", "staging", "1674170951")
	require.NoError(t, err)

	requestAsset := func(withRolloutHeaders bool) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", assetURL, nil)
		r.Header.Set("expo-channel-name", "staging")
		r.Header.Set("expo-app-id", "test-app-id")
		if withRolloutHeaders {
			r.Header.Set("EAS-Client-ID", "d8a43b52-9b1b-4b6a-9e57-52a2ce271244")
			r.Header.Set("Expo-Requested-Update-ID", "04b793a0-b6ab-fd4f-308c-b91d812adec2")
		}
		testContainer().ExpoProtocolHandler.HandleAssets(w, r)
		return w
	}

	baselineResponse := requestAsset(false)
	withHeadersResponse := requestAsset(true)
	assert.Equal(t, 200, baselineResponse.Code)
	assert.Equal(t, 200, withHeadersResponse.Code)
	assert.Equal(t, "application/javascript", withHeadersResponse.Header().Get("Content-Type"))
	assert.Equal(t, baselineResponse.Body.String(), withHeadersResponse.Body.String(), "the asset body must be byte-identical with and without rollout headers")

	expectedContent, err := os.ReadFile(filepath.Join(projectRoot, "/test/test-updates/test-app-id/branch-1/1/1674170951/bundles/android-82adadb1fb6e489d04ad95fd79670deb.js"))
	require.NoError(t, err)
	assert.Equal(t, string(expectedContent), withHeadersResponse.Body.String(), "the served asset must stay the exact branch-1/1/1674170951 file")
}
