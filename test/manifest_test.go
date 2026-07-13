package test

import (
	"encoding/json"
	"expo-open-ota/internal/bucket"
	cache2 "expo-open-ota/internal/cache"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/types"
	"expo-open-ota/internal/update"
	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNotValidChannelForManifest(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	q := "http://localhost:3000/manifest"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "ios")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-channel-name", "bad_channel")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			isFetchExpoUsername := req.Header.Get("operationName") == "FetchExpoUserAccountInformations"
			isFetchExpoChannelMapping := req.Header.Get("operationName") == "FetchExpoChannelMapping"
			if isFetchExpoUsername {
				return MockExpoAccountResponse(map[string]interface{}{
					"id":       "test_id",
					"username": "test_username",
					"email":    "test_email",
				})
			}
			if isFetchExpoChannelMapping {
				return httpmock.NewStringResponse(http.StatusInternalServerError, ""), nil

			}
			return nil, nil
		})
	handlers.ManifestHandler(w, r)
	assert.Equal(t, 500, w.Code, "Expected status code 500 for an invalid branch")
	assert.Equal(t, "Error fetching channel mapping: GraphQL request failed with status: 500 message: \n", w.Body.String())
}

func TestNotMappedChannelForManifest(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	q := "http://localhost:3000/manifest"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "ios")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-channel-name", "bad_channel")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			isFetchExpoUsername := req.Header.Get("operationName") == "FetchExpoUserAccountInformations"
			isFetchExpoChannelMapping := req.Header.Get("operationName") == "FetchExpoChannelMapping"
			if isFetchExpoUsername {
				return MockExpoAccountResponse(map[string]interface{}{
					"id":       "test_id",
					"username": "test_username",
					"email":    "test_email",
				})
			}
			if isFetchExpoChannelMapping {
				return MockExpoChannelMapping([]map[string]interface{}{
					{
						"id":   "branch-1-id",
						"name": "branch-1",
					},
					{
						"id":   "branch-2-id",
						"name": "branch-2",
					},
				}, map[string]interface{}{
					"id":   "bad_channel_id",
					"name": "bad_channel",
					"branchMapping": StringifyBranchMapping(map[string]interface{}{
						"version": 0,
						"data":    []map[string]interface{}{},
					}),
				})

			}
			return nil, nil
		})
	handlers.ManifestHandler(w, r)
	assert.Equal(t, 404, w.Code, "Expected status code 404 for an unmapped channel")
	assert.Equal(t, "No branch mapping found\n", w.Body.String(), "Expected 'No branch mapping found' message")
}

func TestNotValidProtocolVersionsForManifest(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	q := "http://localhost:3000/manifest"

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "ios")
	r.Header.Add("expo-channel-name", "staging")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-protocol-version", "invalid")
	r.Header.Add("expo-expect-signature", "true")
	mockWorkingExpoResponse("staging")
	handlers.ManifestHandler(w, r)
	assert.Equal(t, 400, w.Code, "Expected status code 400 for an invalid protocole version")
	assert.Equal(t, "Invalid protocol version\n", w.Body.String(), "Expected 'Invalid protocol version' message")
}

func TestNotValidPlatformForManifest(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	q := "http://localhost:3000/manifest"

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "bad-platform")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-channel-name", "staging")
	mockWorkingExpoResponse("staging")
	handlers.ManifestHandler(w, r)
	assert.Equal(t, 400, w.Code, "Expected status code 400 for an invalid platform")
	assert.Equal(t, "Invalid platform\n", w.Body.String(), "Expected 'IInvalid platform' message")
}

func TestNotValidRuntimeVersionForManifest(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	q := "http://localhost:3000/manifest"

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "ios")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-channel-name", "staging")

	mockWorkingExpoResponse("staging")
	handlers.ManifestHandler(w, r)
	assert.Equal(t, 400, w.Code, "Expected status code 400 when runtime version is not provided")
	assert.Equal(t, "No runtime version provided\n", w.Body.String(), "Expected 'No runtime version provided' message")
}

func TestNotValidCertificatesForManifest(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	projectRoot, _ := findProjectRoot()
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "/test/test-updates"))
	os.Setenv("EXPO_APP_ID", "EXPO_APP_ID")
	os.Setenv("EXPO_ACCESS_TOKEN", "EXPO_ACCESS_TOKEN")
	os.Setenv("PUBLIC_LOCAL_EXPO_KEY_PATH", filepath.Join(projectRoot, "/test/keys/not.pem"))
	os.Setenv("PRIVATE_LOCAL_EXPO_KEY_PATH", filepath.Join(projectRoot, "/test/keys/exists.pem"))

	q := "http://localhost:3000/manifest"

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "ios")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-channel-name", "staging")
	mockWorkingExpoResponse("staging")
	handlers.ManifestHandler(w, r)

	assert.Equal(t, 500, w.Code, "Expected status code 500 when certificates are not valid")
	assert.Equal(t, "Error signing content\n", w.Body.String(), "Expected 'Error signing content' message")
}

func TestNoUpdatesForManifest(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	q := "http://localhost:3000/manifest"

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "ios")
	r.Header.Add("expo-runtime-version", "nop")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-channel-name", "staging")
	mockWorkingExpoResponse("staging")
	handlers.ManifestHandler(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200 when manifest is retrieved")
	parts, err := ParseMultipartMixedResponse(w.Header().Get("Content-Type"), w.Body.Bytes())
	if err != nil {
		t.Errorf("Error parsing response: %v", err)
	}
	assert.Equal(t, 1, len(parts), "Expected 1 parts in the response")

	manifestPart := parts[0]

	assert.Equal(t, true, IsMultipartPartWithName(manifestPart, "directive"), "Expected a part with name 'manifest'")
	body := manifestPart.Body

	signature := manifestPart.Headers["Expo-Signature"]
	assert.NotNil(t, signature, "Expected a signature in the response")
	assert.NotEqual(t, "", signature, "Expected a signature in the response")
	validSignature := ValidateSignatureHeader(signature, body)
	assert.Equal(t, true, validSignature, "Expected a valid signature")

	var directive types.RollbackDirective
	err = json.Unmarshal([]byte(body), &directive)
	if err != nil {
		t.Errorf("Error parsing json body: %v", err)
	}
	assert.Equal(t, "noUpdateAvailable", directive.Type, "noUpdateAvailable")
}

func TestSkippingNotValidUpdatesAndCache(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			isFetchExpoUsername := req.Header.Get("operationName") == "FetchExpoUserAccountInformations"
			isFetchExpoChannelMapping := req.Header.Get("operationName") == "FetchExpoChannelMapping"

			if isFetchExpoUsername {
				return MockExpoAccountResponse(map[string]interface{}{
					"id":       "test_id",
					"username": "test_username",
					"email":    "test_email",
				})
			}

			if isFetchExpoChannelMapping {
				return MockExpoChannelMapping(
					[]map[string]interface{}{
						{
							"id":   "branch-1-id",
							"name": "branch-1",
						},
						{
							"id":   "branch-2-id",
							"name": "branch-2",
						},
						{
							"id":   "branch-3-id",
							"name": "branch-3",
						},
						{
							"id":   "branch-4-id",
							"name": "branch-4",
						},
					},
					map[string]interface{}{
						"id":   "staging-id",
						"name": "staging",
						"branchMapping": StringifyBranchMapping(map[string]interface{}{
							"version": 0,
							"data": []map[string]interface{}{
								{
									"branchId":           "branch-4-id",
									"branchMappingLogic": "true",
								},
							},
						}),
					},
				)
			}

			return httpmock.NewStringResponse(404, "Unknown operation"), nil
		})
	lastUpdate, err := update.GetLatestUpdateBundlePathForRuntimeVersion("branch-4", "1", "android")
	if err != nil {
		t.Errorf("Error getting latest update: %v", err)
	}
	assert.Equal(t, "1674170951", lastUpdate.UpdateId, "Expected a specific update id")
	resolvedBucket := bucket.GetBucket()
	file, _ := resolvedBucket.GetFile(*lastUpdate, ".check")
	defer file.Reader.Close()
	cache := cache2.GetCache()
	cacheKey := update.ComputeLastUpdateCacheKey("branch-4", "1", "android")
	value := cache.Get(cacheKey)
	assert.Equal(t, "{\"branch\":\"branch-4\",\"runtimeVersion\":\"1\",\"updateId\":\"1674170951\",\"createdAt\":1674170951000000}", value, "Expected a specific value")
	assert.NotNil(t, file.Reader, "Expected a file")
}

func TestValidRequestForStagingManifest(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockWorkingExpoResponse("staging")

	q := "http://localhost:3000/manifest"

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "android")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-channel-name", "staging")
	handlers.ManifestHandler(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200 when manifest is retrieved")
	parts, err := ParseMultipartMixedResponse(w.Header().Get("Content-Type"), w.Body.Bytes())
	if err != nil {
		t.Errorf("Error parsing response: %v", err)
	}
	assert.Equal(t, 1, len(parts), "Expected 1 parts in the response")

	manifestPart := parts[0]

	assert.Equal(t, true, IsMultipartPartWithName(manifestPart, "manifest"), "Expected a part with name 'manifest'")
	body := manifestPart.Body

	signature := manifestPart.Headers["Expo-Signature"]
	assert.NotNil(t, signature, "Expected a signature in the response")
	assert.NotEqual(t, "", signature, "Expected a signature in the response")
	validSignature := ValidateSignatureHeader(signature, body)
	assert.Equal(t, true, validSignature, "Expected a valid signature")
	var updateManifest types.UpdateManifest
	err = json.Unmarshal([]byte(body), &updateManifest)
	if err != nil {
		t.Errorf("Error parsing json body: %v", err)
	}
	assert.Equal(t, "1990-01-01T00:00:00.000Z", updateManifest.CreatedAt, "Expected a specific created at date")
	assert.Equal(t, "1", updateManifest.RunTimeVersion, "Expected a specific runtime version")
	assert.Equal(t, json.RawMessage("{\"branch\":\"branch-1\"}"), updateManifest.Metadata, "Expected branch in metadata")
	assert.Equal(t, "{\"id\":\"04b793a0-b6ab-fd4f-308c-b91d812adec2\",\"createdAt\":\"1990-01-01T00:00:00.000Z\",\"runtimeVersion\":\"1\",\"metadata\":{\"branch\":\"branch-1\"},\"assets\":[{\"hash\":\"JCcs2u_4LMX6zazNmCpvBbYMRQRwS7-UwZpjiGWYgLs\",\"key\":\"4f1cb2cac2370cd5050681232e8575a8\",\"fileExtension\":\".png\",\"contentType\":\"application/javascript\",\"url\":\"http://localhost:3000/assets?asset=assets%2F4f1cb2cac2370cd5050681232e8575a8\\u0026branch=branch-1\\u0026platform=android\\u0026runtimeVersion=1\"}],\"launchAsset\":{\"hash\":\"t3kWQ00Lhn5qCGGhNNMxiD_pcTO_4d7I_1zO3S5Me5k\",\"key\":\"82adadb1fb6e489d04ad95fd79670deb\",\"fileExtension\":\".bundle\",\"contentType\":\"\",\"url\":\"http://localhost:3000/assets?asset=bundles%2Fandroid-82adadb1fb6e489d04ad95fd79670deb.js\\u0026branch=branch-1\\u0026platform=android\\u0026runtimeVersion=1\"},\"extra\":{\"expoClient\":{\"name\":\"expo-updates-client\",\"slug\":\"expo-updates-client\",\"owner\":\"anonymous\",\"version\":\"1.0.0\",\"orientation\":\"portrait\",\"icon\":\"./assets/icon.png\",\"splash\":{\"image\":\"./assets/splash.png\",\"resizeMode\":\"contain\",\"backgroundColor\":\"#ffffff\"},\"runtimeVersion\":\"1\",\"updates\":{\"url\":\"http://localhost:3000/api/manifest\",\"enabled\":true,\"fallbackToCacheTimeout\":30000},\"assetBundlePatterns\":[\"**/*\"],\"ios\":{\"supportsTablet\":true,\"bundleIdentifier\":\"com.test.expo-updates-client\"},\"android\":{\"adaptiveIcon\":{\"foregroundImage\":\"./assets/adaptive-icon.png\",\"backgroundColor\":\"#FFFFFF\"},\"package\":\"com.test.expoupdatesclient\"},\"web\":{\"favicon\":\"./assets/favicon.png\"},\"sdkVersion\":\"47.0.0\",\"platforms\":[\"ios\",\"android\",\"web\"],\"currentFullName\":\"@anonymous/expo-updates-client\",\"originalFullName\":\"@anonymous/expo-updates-client\"},\"branch\":\"branch-1\"}}", body)
}

func TestNoUpdatesResponseForManifest(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockWorkingExpoResponse("staging")

	q := "http://localhost:3000/manifest"

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "ios")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-current-update-id", "04b793a0-b6ab-fd4f-308c-b91d812adec2")
	r.Header.Add("expo-channel-name", "staging")
	handlers.ManifestHandler(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200 when manifest is retrieved")
	parts, err := ParseMultipartMixedResponse(w.Header().Get("Content-Type"), w.Body.Bytes())
	if err != nil {
		t.Errorf("Error parsing response: %v", err)
	}
	assert.Equal(t, 1, len(parts), "Expected 1 parts in the response")

	manifestPart := parts[0]

	assert.Equal(t, true, IsMultipartPartWithName(manifestPart, "directive"), "Expected a part with name 'manifest'")
	body := manifestPart.Body

	signature := manifestPart.Headers["Expo-Signature"]
	assert.NotNil(t, signature, "Expected a signature in the response")
	assert.NotEqual(t, "", signature, "Expected a signature in the response")
	validSignature := ValidateSignatureHeader(signature, body)
	assert.Equal(t, true, validSignature, "Expected a valid signature")

	var directive types.RollbackDirective
	err = json.Unmarshal([]byte(body), &directive)
	if err != nil {
		t.Errorf("Error parsing json body: %v", err)
	}
	assert.Equal(t, "noUpdateAvailable", directive.Type, "noUpdateAvailable")
}

func TestRollbackResponseforManifest(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			isFetchExpoUsername := req.Header.Get("operationName") == "FetchExpoUserAccountInformations"
			isFetchExpoChannelMapping := req.Header.Get("operationName") == "FetchExpoChannelMapping"

			if isFetchExpoUsername {
				return MockExpoAccountResponse(map[string]interface{}{
					"id":       "test_id",
					"username": "test_username",
					"email":    "test_email",
				})
			}

			if isFetchExpoChannelMapping {
				return MockExpoChannelMapping(
					[]map[string]interface{}{
						{
							"id":   "branch-1-id",
							"name": "branch-1",
						},
						{
							"id":   "branch-2-id",
							"name": "branch-2",
						},
						{
							"id":   "branch-3-id",
							"name": "branch-3",
						},
					},
					map[string]interface{}{
						"id":   "rollbackenv-id",
						"name": "rollbackenv",
						"branchMapping": StringifyBranchMapping(map[string]interface{}{
							"version": 0,
							"data": []map[string]interface{}{
								{
									"branchId":           "branch-3-id",
									"branchMappingLogic": "true",
								},
							},
						}),
					},
				)
			}

			return httpmock.NewStringResponse(404, "Unknown operation"), nil
		})
	q := "http://localhost:3000/manifest"

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "ios")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-current-update-id", "04b793a0-b6ab-fd4f-308c-b91d812adec2")
	r.Header.Add("expo-embedded-update-id", "embedded-update-id")
	r.Header.Add("expo-channel-name", "rollbackenv")
	handlers.ManifestHandler(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200 when manifest is retrieved")
	parts, err := ParseMultipartMixedResponse(w.Header().Get("Content-Type"), w.Body.Bytes())
	if err != nil {
		t.Errorf("Error parsing response: %v", err)
	}
	assert.Equal(t, 1, len(parts), "Expected 1 parts in the response")

	manifestPart := parts[0]

	assert.Equal(t, true, IsMultipartPartWithName(manifestPart, "directive"), "Expected a part with name 'manifest'")
	body := manifestPart.Body

	signature := manifestPart.Headers["Expo-Signature"]
	assert.NotNil(t, signature, "Expected a signature in the response")
	assert.NotEqual(t, "", signature, "Expected a signature in the response")
	validSignature := ValidateSignatureHeader(signature, body)
	assert.Equal(t, true, validSignature, "Expected a valid signature")

	var directive types.RollbackDirective
	err = json.Unmarshal([]byte(body), &directive)
	if err != nil {
		t.Errorf("Error parsing json body: %v", err)
	}
	assert.Equal(t, "rollBackToEmbedded", directive.Type, "rollBackToEmbedded")
}

func TestValidRequestForProductionManifest(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			isFetchSelfExpoUsername := req.Header.Get("operationName") == "FetchSelfExpoUsername"
			isFetchExpoChannelMapping := req.Header.Get("operationName") == "FetchExpoChannelMapping"

			if isFetchSelfExpoUsername {
				return MockExpoAccountResponse(map[string]interface{}{
					"id":       "test_id",
					"username": "test_username",
					"email":    "test_email",
				})
			}

			if isFetchExpoChannelMapping {
				return MockExpoChannelMapping(
					[]map[string]interface{}{
						{
							"id":   "branch-1-id",
							"name": "branch-1",
						},
						{
							"id":   "branch-2-id",
							"name": "branch-2",
						},
						{
							"id":   "branch-3-id",
							"name": "branch-3",
						},
					},
					map[string]interface{}{
						"id":   "production-id",
						"name": "production",
						"branchMapping": StringifyBranchMapping(map[string]interface{}{
							"version": 0,
							"data": []map[string]interface{}{
								{
									"branchId":           "branch-2-id",
									"branchMappingLogic": "true",
								},
							},
						}),
					},
				)
			}

			return httpmock.NewStringResponse(404, "Unknown operation"), nil
		})

	q := "http://localhost:3000/manifest"

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "ios")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-channel-name", "production")
	handlers.ManifestHandler(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200 when manifest is retrieved")
	parts, err := ParseMultipartMixedResponse(w.Header().Get("Content-Type"), w.Body.Bytes())
	if err != nil {
		t.Errorf("Error parsing response: %v", err)
	}
	assert.Equal(t, 1, len(parts), "Expected 1 parts in the response")

	manifestPart := parts[0]

	assert.Equal(t, true, IsMultipartPartWithName(manifestPart, "manifest"), "Expected a part with name 'manifest'")
	body := manifestPart.Body

	signature := manifestPart.Headers["Expo-Signature"]
	assert.NotNil(t, signature, "Expected a signature in the response")
	assert.NotEqual(t, "", signature, "Expected a signature in the response")
	validSignature := ValidateSignatureHeader(signature, body)
	assert.Equal(t, true, validSignature, "Expected a valid signature")
	var updateManifest types.UpdateManifest
	err = json.Unmarshal([]byte(body), &updateManifest)
	if err != nil {
		t.Errorf("Error parsing json body: %v", err)
	}
	assert.Equal(t, "1990-01-01T00:00:00.000Z", updateManifest.CreatedAt, "Expected a specific created at date")
	assert.Equal(t, "1", updateManifest.RunTimeVersion, "Expected a specific runtime version")
	assert.Equal(t, json.RawMessage("{\"branch\":\"branch-2\"}"), updateManifest.Metadata, "Expected branch in metadata")
	assert.Equal(t, "{\"id\":\"68e096e2-a619-9d56-7f7c-89f97bc27312\",\"createdAt\":\"1990-01-01T00:00:00.000Z\",\"runtimeVersion\":\"1\",\"metadata\":{\"branch\":\"branch-2\"},\"assets\":[{\"hash\":\"JCcs2u_4LMX6zazNmCpvBbYMRQRwS7-UwZpjiGWYgLs\",\"key\":\"4f1cb2cac2370cd5050681232e8575a8\",\"fileExtension\":\".png\",\"contentType\":\"application/javascript\",\"url\":\"http://localhost:3000/assets?asset=assets%2F4f1cb2cac2370cd5050681232e8575a8\\u0026branch=branch-2\\u0026platform=ios\\u0026runtimeVersion=1\"}],\"launchAsset\":{\"hash\":\"vH93RoNbdzk_2emr38L0ZVYJVBTPcspX5-5DXLUkiQ8\",\"key\":\"e44a25e2b1df198470a04adc1dd82e4e\",\"fileExtension\":\".bundle\",\"contentType\":\"\",\"url\":\"http://localhost:3000/assets?asset=_expo%2Fstatic%2Fjs%2Fios%2FAppEntry-546b83fc2035b34c5f2dbd9bb04a2478.hbc\\u0026branch=branch-2\\u0026platform=ios\\u0026runtimeVersion=1\"},\"extra\":{\"expoClient\":{\"name\":\"expo-updates-client\",\"slug\":\"expo-updates-client\",\"owner\":\"anonymous\",\"version\":\"1.0.0\",\"orientation\":\"portrait\",\"icon\":\"./assets/icon.png\",\"splash\":{\"image\":\"./assets/splash.png\",\"resizeMode\":\"contain\",\"backgroundColor\":\"#ffffff\"},\"runtimeVersion\":\"1\",\"updates\":{\"url\":\"http://localhost:3000/api/manifest\",\"enabled\":true,\"fallbackToCacheTimeout\":30000},\"assetBundlePatterns\":[\"**/*\"],\"ios\":{\"supportsTablet\":true,\"bundleIdentifier\":\"com.test.expo-updates-client\"},\"android\":{\"adaptiveIcon\":{\"foregroundImage\":\"./assets/adaptive-icon.png\",\"backgroundColor\":\"#FFFFFF\"},\"package\":\"com.test.expoupdatesclient\"},\"web\":{\"favicon\":\"./assets/favicon.png\"},\"plugins\":[[\"expo-build-properties\",{\"android\":{\"usesCleartextTraffic\":true},\"ios\":{}}]],\"sdkVersion\":\"52.0.0\",\"platforms\":[\"ios\",\"android\"],\"currentFullName\":\"@anonymous/expo-updates-client\",\"originalFullName\":\"@anonymous/expo-updates-client\"},\"branch\":\"branch-2\"}}", body)
}

func TestEmptyRequestForAndroid(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			isFetchSelfExpoUsername := req.Header.Get("operationName") == "FetchSelfExpoUsername"
			isFetchExpoChannelMapping := req.Header.Get("operationName") == "FetchExpoChannelMapping"

			if isFetchSelfExpoUsername {
				return MockExpoAccountResponse(map[string]interface{}{
					"id":       "test_id",
					"username": "test_username",
					"email":    "test_email",
				})
			}

			if isFetchExpoChannelMapping {
				return MockExpoChannelMapping(
					[]map[string]interface{}{
						{
							"id":   "branch-1-id",
							"name": "branch-1",
						},
						{
							"id":   "branch-2-id",
							"name": "branch-2",
						},
						{
							"id":   "branch-3-id",
							"name": "branch-3",
						},
					},
					map[string]interface{}{
						"id":   "production-id",
						"name": "production",
						"branchMapping": StringifyBranchMapping(map[string]interface{}{
							"version": 0,
							"data": []map[string]interface{}{
								{
									"branchId":           "branch-3-id",
									"branchMappingLogic": "true",
								},
							},
						}),
					},
				)
			}

			return httpmock.NewStringResponse(404, "Unknown operation"), nil
		})

	q := "http://localhost:3000/manifest"

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "android")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-channel-name", "production")
	handlers.ManifestHandler(w, r)
	assert.Equal(t, 200, w.Code, "Expected status code 200 when manifest is retrieved")
	parts, err := ParseMultipartMixedResponse(w.Header().Get("Content-Type"), w.Body.Bytes())
	if err != nil {
		t.Errorf("Error parsing response: %v", err)
	}
	assert.Equal(t, 1, len(parts), "Expected 1 parts in the response")

	manifestPart := parts[0]

	assert.Equal(t, true, IsMultipartPartWithName(manifestPart, "directive"), "Expected a part with name 'directive'")
	body := manifestPart.Body

	signature := manifestPart.Headers["Expo-Signature"]
	assert.NotNil(t, signature, "Expected a signature in the response")
	assert.NotEqual(t, "", signature, "Expected a signature in the response")
	validSignature := ValidateSignatureHeader(signature, body)
	assert.Equal(t, true, validSignature, "Expected a valid signature")
	var updateManifest types.UpdateManifest
	err = json.Unmarshal([]byte(body), &updateManifest)
	if err != nil {
		t.Errorf("Error parsing json body: %v", err)
	}
	assert.Equal(t, "{\"type\":\"noUpdateAvailable\"}", body)
}

func TestPreWarmManifestCache(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockWorkingExpoResponse("staging")

	cache := cache2.GetCache()

	// Verify caches are empty before prewarm
	lastUpdateKey := update.ComputeLastUpdateCacheKey("branch-1", "1", "android")
	assert.Equal(t, "", cache.Get(lastUpdateKey), "lastUpdate cache should be empty before prewarm")

	// Run PreWarm synchronously (not as goroutine) for testing
	update.PreWarmManifestCache("branch-1", "1", "android")

	// Verify lastUpdate cache was populated
	lastUpdateCached := cache.Get(lastUpdateKey)
	assert.NotEqual(t, "", lastUpdateCached, "lastUpdate cache should be populated after prewarm")

	// Verify metadata cache was populated
	var cachedUpdate types.Update
	err := json.Unmarshal([]byte(lastUpdateCached), &cachedUpdate)
	assert.NoError(t, err)
	metadataKey := update.ComputeMetadataCacheKey("branch-1", "1", cachedUpdate.UpdateId)
	assert.NotEqual(t, "", cache.Get(metadataKey), "metadata cache should be populated after prewarm")

	// Verify manifest cache was populated
	manifestKey := update.ComputeUpdataManifestCacheKey("branch-1", "1", cachedUpdate.UpdateId, "android")
	assert.NotEqual(t, "", cache.Get(manifestKey), "manifest cache should be populated after prewarm")
}

func TestPreWarmUpdateManifestCacheDoesNotPopulateLastUpdateCache(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockWorkingExpoResponse("staging")

	cache := cache2.GetCache()
	checkedUpdate := types.Update{
		Branch:         "branch-1",
		RuntimeVersion: "1",
		UpdateId:       "1674170951",
	}

	lastUpdateKey := update.ComputeLastUpdateCacheKey("branch-1", "1", "android")
	assert.Equal(t, "", cache.Get(lastUpdateKey), "lastUpdate cache should be empty before prewarm")

	update.PreWarmUpdateManifestCache(checkedUpdate, "android")

	manifestKey := update.ComputeUpdataManifestCacheKey("branch-1", "1", "1674170951", "android")
	assert.NotEqual(t, "", cache.Get(manifestKey), "manifest cache should be populated after prewarm")
	assert.Equal(t, "", cache.Get(lastUpdateKey), "prewarming a known update should not populate lastUpdate cache")
}
