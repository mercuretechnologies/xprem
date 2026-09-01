package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"xprem/internal/assets"
	"xprem/internal/cdn"
	"xprem/internal/types"

	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLegacyFolderStorageStillServed is the backward-compatibility contract for
// updates published before CAS existed: files in the update folder (assets/,
// bundles/), no asset mapping, manifests addressing assets by path. branch-legacy
// is the one fixture kept in that layout; everything else is CAS. The subtests
// cover the folder-path resolution rules that only exist on this legacy path.
func TestLegacyFolderStorageStillServed(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			switch req.Header.Get("operationName") {
			case "FetchSelfExpoUsername":
				return MockExpoAccountResponse(map[string]interface{}{
					"id": "test_id", "username": "test_username", "email": "test_email",
				})
			case "FetchExpoChannelMapping":
				return MockExpoChannelMapping(
					[]map[string]interface{}{{"id": "branch-legacy-id", "name": "branch-legacy"}},
					map[string]interface{}{
						"id":   "legacy-id",
						"name": "legacy",
						"branchMapping": StringifyBranchMapping(map[string]interface{}{
							"version": 0,
							"data": []map[string]interface{}{
								{"branchId": "branch-legacy-id", "branchMappingLogic": "true"},
							},
						}),
					},
				)
			}
			return httpmock.NewStringResponse(404, "Unknown operation"), nil
		})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://localhost:3000/manifest", nil)
	r.Header.Add("expo-platform", "android")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-channel-name", "legacy")
	r.Header.Add("expo-app-id", "test-app-id")
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)
	require.Equal(t, 200, w.Code, "Expected status code 200")

	parts, err := ParseMultipartMixedResponse(w.Header().Get("Content-Type"), w.Body.Bytes())
	require.NoError(t, err, "Error parsing multipart response")
	require.Len(t, parts, 1)
	require.True(t, IsMultipartPartWithName(parts[0], "manifest"), "Expected a part with name 'manifest'")
	var manifest types.UpdateManifest
	require.NoError(t, json.Unmarshal([]byte(parts[0].Body), &manifest))
	assert.Equal(t, "04b793a0-b6ab-fd4f-308c-b91d812adec3", manifest.Id)

	// Without a mapping, assets are addressed by path and pinned to the update.
	launchURL := manifest.LaunchAsset.Url
	parsedLaunchURL, err := url.Parse(launchURL)
	require.NoError(t, err)
	launchQuery := parsedLaunchURL.Query()
	assert.Equal(t, "bundles/android-82adadb1fb6e489d04ad95fd79670deb.js", launchQuery.Get("asset"))
	assert.Equal(t, "branch-legacy", launchQuery.Get("branch"))
	assert.Equal(t, "1674170951", launchQuery.Get("updateId"))
	assert.Empty(t, launchQuery.Get("h"), "a legacy manifest must not hand out blob URLs")

	wAsset := httptest.NewRecorder()
	rAsset := httptest.NewRequest("GET", launchURL, nil)
	rAsset.Header.Set("expo-channel-name", "legacy")
	rAsset.Header.Set("expo-app-id", "test-app-id")
	testContainer().ExpoProtocolHandler.HandleAssets(wAsset, rAsset)
	assert.Equal(t, 200, wAsset.Code, "Expected status code 200")
	assert.Equal(t, "application/javascript", wAsset.Header().Get("Content-Type"))

	projectRoot, err := findProjectRoot()
	require.NoError(t, err)
	expectedContent, err := os.ReadFile(filepath.Join(projectRoot,
		"/test/test-updates/test-app-id/branch-legacy/1/1674170951/bundles/android-82adadb1fb6e489d04ad95fd79670deb.js"))
	require.NoError(t, err)
	assert.Equal(t, string(expectedContent), wAsset.Body.String(), "the served bytes must be the update folder's file")

	// The folder-path resolution rules below only exist on this legacy path:
	// blob requests carry no asset name, no runtime version and no whitelist.
	legacyUpdate := &types.Update{
		AppId:          "test-app-id",
		Branch:         "branch-legacy",
		RuntimeVersion: "1",
		UpdateId:       "1674170951",
	}
	runBothAssetHandlers := func(t *testing.T, request assets.AssetsRequest, wantStatus int, wantBody string) {
		t.Helper()
		fileResponse, err := assets.HandleAssetsWithFile(request)
		assert.Nil(t, err)
		assert.Equal(t, wantStatus, fileResponse.StatusCode)
		assert.Equal(t, wantBody, string(fileResponse.Body))
		urlResponse, err := assets.HandleAssetsWithURL(request, &cdn.GenericCDN{})
		assert.Nil(t, err)
		assert.Equal(t, wantStatus, urlResponse.StatusCode)
		assert.Empty(t, urlResponse.URL)
	}

	t.Run("asset name is required", func(t *testing.T) {
		runBothAssetHandlers(t, assets.AssetsRequest{
			AppId: "test-app-id", Branch: "branch-legacy", AssetName: "",
			RuntimeVersion: "1", Platform: "android", RequestID: "test",
		}, 400, "No asset name provided")
	})

	t.Run("runtime version is required", func(t *testing.T) {
		runBothAssetHandlers(t, assets.AssetsRequest{
			AppId: "test-app-id", Branch: "branch-legacy", AssetName: "/assets/4f1cb2cac2370cd5050681232e8575a8",
			RuntimeVersion: "", Platform: "android", RequestID: "test",
		}, 400, "No runtime version provided")
	})

	t.Run("no update resolves to 404", func(t *testing.T) {
		// An unresolved update (unknown branch, or a runtime version nothing
		// was built for) reaches the handlers as a nil Update.
		runBothAssetHandlers(t, assets.AssetsRequest{
			AppId: "test-app-id", Branch: "branch-legacy", AssetName: "/assets/4f1cb2cac2370cd5050681232e8575a8",
			RuntimeVersion: "never", Platform: "android", RequestID: "test",
		}, 404, "No update found")
	})

	t.Run("path traversal is rejected", func(t *testing.T) {
		runBothAssetHandlers(t, assets.AssetsRequest{
			AppId: "test-app-id", Branch: "branch-legacy", AssetName: "../../etc/passwd",
			RuntimeVersion: "1", Platform: "android", RequestID: "test", Update: legacyUpdate,
		}, 404, "Asset not found")
	})

	t.Run("unlisted asset is rejected", func(t *testing.T) {
		// A file that exists in the update folder but is not declared by the
		// manifest must be neither redirected to nor proxied.
		runBothAssetHandlers(t, assets.AssetsRequest{
			AppId: "test-app-id", Branch: "branch-legacy", AssetName: "update-metadata.json",
			RuntimeVersion: "1", Platform: "android", RequestID: "test", Update: legacyUpdate,
		}, 404, "Asset not found")
	})

	t.Run("CDN URL keeps the folder layout", func(t *testing.T) {
		os.Setenv("CDN_BASE_URL", "https://cdn.example.com")
		defer os.Unsetenv("CDN_BASE_URL")
		response, err := assets.HandleAssetsWithURL(assets.AssetsRequest{
			AppId: "test-app-id", Branch: "branch-legacy", AssetName: "bundles/android-82adadb1fb6e489d04ad95fd79670deb.js",
			RuntimeVersion: "1", Platform: "android", RequestID: "test", Update: legacyUpdate,
		}, &cdn.GenericCDN{})
		assert.Nil(t, err)
		assert.Equal(t, 200, response.StatusCode)
		assert.Equal(t, "https://cdn.example.com/test-app-id/branch-legacy/1/1674170951/bundles/android-82adadb1fb6e489d04ad95fd79670deb.js", response.URL)
	})
}
