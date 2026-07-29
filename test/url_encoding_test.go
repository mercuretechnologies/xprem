package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const runtimeVersionWithPlus = "1.2.3+ios.1234"

func TestRequestUploadUrlWithEncodedPlusInRuntimeVersion(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))
	sampleUpdatePath := filepath.Join(projectRoot, "test/test-updates/test-app-id/branch-4/1/1674170952")

	u, _ := url.Parse("http://localhost:3000/test-app-id/requestUploadUrl/DO_NOT_USE")
	q := u.Query()
	q.Set("runtimeVersion", runtimeVersionWithPlus)
	q.Set("platform", "android")
	q.Set("commitHash", "abc123")
	u.RawQuery = q.Encode()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", u.String(), nil)
	r.Header.Set("Authorization", "Bearer expo_test_token")

	uploadRequestsInput := ComputeUploadRequestsInput(sampleUpdatePath)
	uploadRequestsInputJSON, err := json.Marshal(uploadRequestsInput)
	require.NoError(t, err)
	r.Body = io.NopCloser(bytes.NewReader(uploadRequestsInputJSON))

	serveThroughRouter(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200")
	assert.NotEmpty(t, w.Header().Get("expo-update-id"), "Expected non-empty update ID")

	// Verify the files were stored under the correct runtimeVersion path (with +, not space)
	updateId := w.Header().Get("expo-update-id")
	expectedPath := filepath.Join(projectRoot, "updates", "test-app-id", "DO_NOT_USE", runtimeVersionWithPlus, updateId, "update-metadata.json")
	_, err = os.Stat(expectedPath)
	assert.NoError(t, err, "Expected update-metadata.json to be stored under runtimeVersion with + character")
}

func TestRollbackWithEncodedPlusInRuntimeVersion(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForRequestUploadUrlTest("staging")
	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "./updates"))

	u, _ := url.Parse("http://localhost:3000/test-app-id/rollback/DO_NOT_USE")
	q := u.Query()
	q.Set("runtimeVersion", runtimeVersionWithPlus)
	q.Set("platform", "ios")
	q.Set("commitHash", "abc123")
	u.RawQuery = q.Encode()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", u.String(), nil)
	r.Header.Set("Authorization", "Bearer expo_test_token")

	serveThroughRouter(w, r)
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

	assert.Equal(t, runtimeVersionWithPlus, body.RuntimeVersion, "Expected runtimeVersion to contain + character, not space")
	assert.NotEmpty(t, body.UpdateId)

	lastUpdate, err := testLatestUpdate("test-app-id", "DO_NOT_USE", runtimeVersionWithPlus, "ios")
	require.NoError(t, err)
	assert.NotNil(t, lastUpdate, "Expected to find the rollback update using runtimeVersion with +")
	assert.Equal(t, body.UpdateId, lastUpdate.UpdateId)
}

func TestManifestWithEncodedPlusInRuntimeVersion(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockWorkingExpoResponse("staging")
	projectRoot, err := findProjectRoot()
	require.NoError(t, err)

	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "/test/test-updates"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://localhost:3000/manifest", nil)
	r.Header.Add("expo-platform", "android")
	r.Header.Add("expo-runtime-version", runtimeVersionWithPlus)
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-channel-name", "staging")
	r.Header.Add("expo-app-id", "test-app-id")

	testContainer().ExpoProtocolHandler.HandleManifest(w, r)

	// No updates exist for this runtimeVersion, so we expect a 200 with a "no update available" directive
	assert.Equal(t, 200, w.Code, "Expected status code 200")
}

func TestUnEncodedPlusBecomesSpace(t *testing.T) {
	// This test documents the bug: an unencoded + in a query string is interpreted as a space.
	// The CLI fix (using URL + searchParams.set) prevents this from happening.
	rawURL := "http://localhost:3000/test?runtimeVersion=1.2.3+ios.1234"
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)

	got := parsed.Query().Get("runtimeVersion")
	assert.Equal(t, "1.2.3 ios.1234", got, "Unencoded + in query string should be interpreted as space (this is the bug)")

	// With proper encoding, the + is preserved
	u, _ := url.Parse("http://localhost:3000/test")
	q := u.Query()
	q.Set("runtimeVersion", "1.2.3+ios.1234")
	u.RawQuery = q.Encode()

	parsed2, _ := url.Parse(u.String())
	got2 := parsed2.Query().Get("runtimeVersion")
	assert.Equal(t, "1.2.3+ios.1234", got2, "Properly encoded + should be preserved as +")
}
