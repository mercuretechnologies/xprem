package test

import (
	"context"
	"encoding/json"
	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"xprem/config"
	"xprem/internal/bucket"
	cache2 "xprem/internal/cache"
	crypto2 "xprem/internal/crypto"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/types"
	"xprem/internal/update"
	"xprem/internal/version"
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
	r.Header.Add("expo-app-id", "test-app-id")
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
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)
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
	r.Header.Add("expo-app-id", "test-app-id")
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
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)
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
	r.Header.Add("expo-app-id", "test-app-id")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-protocol-version", "invalid")
	r.Header.Add("expo-expect-signature", "true")
	mockWorkingExpoResponse("staging")
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)
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
	r.Header.Add("expo-app-id", "test-app-id")
	mockWorkingExpoResponse("staging")
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)
	assert.Equal(t, 400, w.Code, "Expected status code 400 for an invalid platform")
	assert.Equal(t, "Invalid platform\n", w.Body.String(), "Expected 'IInvalid platform' message")
}

// legacyClientManifestRequest builds a manifest request shaped exactly like
// the one a v1 binary sends: every header it knew about, and no expo-app-id -
// that header is baked into Expo.plist / AndroidManifest.xml at build time, so
// an already-installed v1 client cannot start sending it without a store
// release. The runtime version resolves to no update, which is the shortest
// path to a signed 200.
func legacyClientManifestRequest() *http.Request {
	r := httptest.NewRequest("GET", "http://localhost:3000/manifest", nil)
	r.Header.Add("expo-platform", "ios")
	r.Header.Add("expo-runtime-version", "nop")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-channel-name", "staging")
	return r
}

// assertServedLegacyApp asserts the response was served, and served as
// test-app-id. The signature check is what makes this meaningful: it only
// validates against test-app-id's key pair, so a 200 signed with it proves the
// request resolved to the legacy app rather than to some empty or default one.
func assertServedLegacyApp(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, 200, w.Code, "A v1 client with no expo-app-id must still be served")
	parts, err := ParseMultipartMixedResponse(w.Header().Get("Content-Type"), w.Body.Bytes())
	if err != nil {
		t.Fatalf("Error parsing response: %v", err)
	}
	assert.Equal(t, 1, len(parts), "Expected 1 part in the response")
	signature := parts[0].Headers["Expo-Signature"]
	assert.NotEmpty(t, signature, "Expected a signature in the response")
	assert.True(t, ValidateSignatureHeader("test-app-id", signature, parts[0].Body),
		"Response must be signed by the legacy app's keys, proving the fallback resolved to it")
}

// TestManifestMissingAppIdHeaderFallsBackToLegacyApp covers the "no header at
// all" branch. A v1 client cannot send expo-app-id, and rejecting it would kill
// its update channel until a store release lands, for an OTA server, the one
// breaking change that defeats the point. EXPO_APP_ID names the only app such a
// deploy has, so the request is not ambiguous and gets served.
func TestManifestMissingAppIdHeaderFallsBackToLegacyApp(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	w := httptest.NewRecorder()
	mockWorkingExpoResponse("staging")
	testContainer().ExpoProtocolHandler.HandleManifest(w, legacyClientManifestRequest())

	assertServedLegacyApp(t, w)
}

// TestManifestEmptyAppIdHeaderFallsBackToLegacyApp, the header is present but
// empty. Must take the same path as missing rather than resolving to the
// empty-string app.
func TestManifestEmptyAppIdHeaderFallsBackToLegacyApp(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	r := legacyClientManifestRequest()
	r.Header.Add("expo-app-id", "")

	w := httptest.NewRecorder()
	mockWorkingExpoResponse("staging")
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)

	assertServedLegacyApp(t, w)
}

// TestManifestMissingAppIdHeaderRejectedWhenFallbackSkipped, the opt-out an
// operator sets once every client ships the header. Header-less requests fail
// again, which is what surfaces the stragglers still running a v1 binary.
func TestManifestMissingAppIdHeaderRejectedWhenFallbackSkipped(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	t.Setenv("SKIP_LEGACY_APP_ID_FALLBACK", "true")

	w := httptest.NewRecorder()
	testContainer().ExpoProtocolHandler.HandleManifest(w, legacyClientManifestRequest())

	assert.Equal(t, 400, w.Code, "Missing expo-app-id must 400 once the fallback is opted out of")
}

// The control-plane shape, no EXPO_APP_ID, so no legacy app to fall back to -
// is not reachable from here: a stateless container refuses to boot without
// EXPO_APP_ID (wire.go log.Fatals on it), and a DB-mode container needs a
// database. config.TestLegacyFallbackAppId covers that env resolution instead,
// and the rejection it feeds into is the same appId == "" branch the
// fallback-skipped test above already exercises.

// TestManifestMalformedAppIdHeader checks the handler rejects values
// that look like path traversal or whitespace-padded ids. Even though
// the registry lookup would 404 them, we want the response to be clean
// (not 500) and not trip any log-injection sensitivities.
func TestManifestMalformedAppIdHeader(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	for _, badId := range []string{"../etc", "a/b", "with\tctrl", "   "} {
		t.Run(badId, func(t *testing.T) {
			q := "http://localhost:3000/manifest"
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", q, nil)
			r.Header.Add("expo-platform", "ios")
			r.Header.Add("expo-runtime-version", "1")
			r.Header.Add("expo-protocol-version", "1")
			r.Header.Add("expo-expect-signature", "true")
			r.Header.Add("expo-channel-name", "staging")
			r.Header.Add("expo-app-id", badId)

			testContainer().ExpoProtocolHandler.HandleManifest(w, r)
			// 400 (malformed) or 404 (not in registry) are both acceptable
			//, the invariant is "no 5xx and no data returned".
			assert.Truef(t, w.Code == 400 || w.Code == 404, "want 400 or 404, got %d", w.Code)
		})
	}
}

// TestUnknownAppIdForManifest locks in the 404-on-unknown-app behaviour so
// we never regress into firing an outbound Expo API call with an empty
// Bearer token, which used to surface as an opaque 500 to the client.
func TestUnknownAppIdForManifest(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	q := "http://localhost:3000/manifest"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "ios")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-channel-name", "staging")
	r.Header.Add("expo-app-id", "this-id-is-not-in-apps-json")

	testContainer().ExpoProtocolHandler.HandleManifest(w, r)
	assert.Equal(t, 404, w.Code, "Unknown app id must fail early with 404")
	assert.Equal(t, "Unknown app id\n", w.Body.String())
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
	r.Header.Add("expo-app-id", "test-app-id")

	mockWorkingExpoResponse("staging")
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)
	assert.Equal(t, 400, w.Code, "Expected status code 400 when runtime version is not provided")
	assert.Equal(t, "No runtime version provided\n", w.Body.String(), "Expected 'No runtime version provided' message")
}

func TestNotValidCertificatesForManifest(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	projectRoot, _ := findProjectRoot()
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "/test/test-updates"))
	// Override the shared config (set by SetValidConfiguration) with a key
	// path that points to a missing file so the signing step fails.
	os.Setenv("EXPO_APP_ID", "test-app-id")
	os.Setenv("EXPO_ACCESS_TOKEN", "EXPO_ACCESS_TOKEN")
	os.Setenv("KEYS_STORAGE_TYPE", "local")
	os.Setenv("PUBLIC_LOCAL_EXPO_KEY_PATH", filepath.Join(projectRoot, "/test/keys/not.pem"))
	os.Setenv("PRIVATE_LOCAL_EXPO_KEY_PATH", filepath.Join(projectRoot, "/test/keys/exists.pem"))
	config.ResetAppsForTest()
	if err := config.LoadAppsFromFlatEnv(); err != nil {
		t.Fatalf("LoadAppsFromFlatEnv: %v", err)
	}
	defer func() {
		SetValidConfiguration()
	}()

	q := "http://localhost:3000/manifest"

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", q, nil)
	r.Header.Add("expo-platform", "ios")
	r.Header.Add("expo-runtime-version", "1")
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-channel-name", "staging")
	r.Header.Add("expo-app-id", "test-app-id")
	mockWorkingExpoResponse("staging")
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)

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
	r.Header.Add("expo-app-id", "test-app-id")
	mockWorkingExpoResponse("staging")
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)
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
	validSignature := ValidateSignatureHeader("test-app-id", signature, body)
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
	// Caching moved from the free resolver into UpdateService.GetLatestUpdate,
	// so drive the service (which also skips invalid updates via the repo) to
	// exercise both the skip logic and the cache write this test asserts on.
	lastUpdate, err := testUpdateService().GetLatestUpdate(context.Background(), "test-app-id", "branch-4", "1", "android")
	if err != nil {
		t.Errorf("Error getting latest update: %v", err)
	}
	assert.Equal(t, "1674170951", lastUpdate.UpdateId, "Expected a specific update id")
	resolvedBucket := bucket.GetBucket()
	file, _ := resolvedBucket.GetFile(*lastUpdate, ".check")
	defer file.Reader.Close()
	cache := cache2.GetCache()
	cacheKey := update.ComputeLastUpdateCacheKey("test-app-id", "branch-4", "1", "android")
	value := cache.Get(cacheKey)
	assert.Equal(t, "{\"appId\":\"test-app-id\",\"branch\":\"branch-4\",\"runtimeVersion\":\"1\",\"updateId\":\"1674170951\",\"createdAt\":1674170951000000}", value, "Expected a specific value")
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
	r.Header.Add("expo-app-id", "test-app-id")
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)
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
	validSignature := ValidateSignatureHeader("test-app-id", signature, body)
	assert.Equal(t, true, validSignature, "Expected a valid signature")
	var updateManifest types.UpdateManifest
	err = json.Unmarshal([]byte(body), &updateManifest)
	if err != nil {
		t.Errorf("Error parsing json body: %v", err)
	}
	assert.Equal(t, "1990-01-01T00:00:00.000Z", updateManifest.CreatedAt, "Expected a specific created at date")
	assert.Equal(t, "1", updateManifest.RunTimeVersion, "Expected a specific runtime version")
	assert.Equal(t, json.RawMessage("{\"branch\":\"branch-1\"}"), updateManifest.Metadata, "Expected branch in metadata")
	assert.Equal(t, "{\"id\":\"04b793a0-b6ab-fd4f-308c-b91d812adec2\",\"createdAt\":\"1990-01-01T00:00:00.000Z\",\"runtimeVersion\":\"1\",\"metadata\":{\"branch\":\"branch-1\"},\"assets\":[{\"hash\":\"JCcs2u_4LMX6zazNmCpvBbYMRQRwS7-UwZpjiGWYgLs\",\"key\":\"4f1cb2cac2370cd5050681232e8575a8\",\"fileExtension\":\".png\",\"contentType\":\"application/javascript\",\"url\":\"http://localhost:3000/assets?asset=assets%2F4f1cb2cac2370cd5050681232e8575a8\\u0026branch=branch-1\\u0026platform=android\\u0026runtimeVersion=1\\u0026updateId=1674170951\"}],\"launchAsset\":{\"hash\":\"t3kWQ00Lhn5qCGGhNNMxiD_pcTO_4d7I_1zO3S5Me5k\",\"key\":\"82adadb1fb6e489d04ad95fd79670deb\",\"fileExtension\":\".bundle\",\"contentType\":\"\",\"url\":\"http://localhost:3000/assets?asset=bundles%2Fandroid-82adadb1fb6e489d04ad95fd79670deb.js\\u0026branch=branch-1\\u0026platform=android\\u0026runtimeVersion=1\\u0026updateId=1674170951\"},\"extra\":{\"expoClient\":{\"name\":\"expo-updates-client\",\"slug\":\"expo-updates-client\",\"owner\":\"anonymous\",\"version\":\"1.0.0\",\"orientation\":\"portrait\",\"icon\":\"./assets/icon.png\",\"splash\":{\"image\":\"./assets/splash.png\",\"resizeMode\":\"contain\",\"backgroundColor\":\"#ffffff\"},\"runtimeVersion\":\"1\",\"updates\":{\"url\":\"http://localhost:3000/api/manifest\",\"enabled\":true,\"fallbackToCacheTimeout\":30000},\"assetBundlePatterns\":[\"**/*\"],\"ios\":{\"supportsTablet\":true,\"bundleIdentifier\":\"com.test.expo-updates-client\"},\"android\":{\"adaptiveIcon\":{\"foregroundImage\":\"./assets/adaptive-icon.png\",\"backgroundColor\":\"#FFFFFF\"},\"package\":\"com.test.expoupdatesclient\"},\"web\":{\"favicon\":\"./assets/favicon.png\"},\"sdkVersion\":\"47.0.0\",\"platforms\":[\"ios\",\"android\",\"web\"],\"currentFullName\":\"@anonymous/expo-updates-client\",\"originalFullName\":\"@anonymous/expo-updates-client\"},\"branch\":\"branch-1\"}}", body)
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
	r.Header.Add("expo-app-id", "test-app-id")
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)
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
	validSignature := ValidateSignatureHeader("test-app-id", signature, body)
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
	r.Header.Add("expo-app-id", "test-app-id")
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)
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
	validSignature := ValidateSignatureHeader("test-app-id", signature, body)
	assert.Equal(t, true, validSignature, "Expected a valid signature")

	var directive types.RollbackDirective
	err = json.Unmarshal([]byte(body), &directive)
	if err != nil {
		t.Errorf("Error parsing json body: %v", err)
	}
	assert.Equal(t, "rollBackToEmbedded", directive.Type, "rollBackToEmbedded")
	// Pin the commitTime. Update.CreatedAt is a duration since the epoch, i.e.
	// nanoseconds; passing it to the millisecond-based NormalizeTimestamp sent
	// every value down the overflow branch and emitted dates thousands of years
	// out. expo-updates reads this field to decide whether to apply the
	// rollback, so a wrong value silently disables it on shipped clients.
	// Expected here is NormalizeTimestamp(1666304169), the branch-3 fixture's
	// own update id, which is second-based, hence 1970.
	assert.Equal(t, "1970-01-20T06:51:44.169Z", directive.Parameters.CommitTime, "unexpected rollback commitTime")
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
	r.Header.Add("expo-app-id", "test-app-id")
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)
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
	validSignature := ValidateSignatureHeader("test-app-id", signature, body)
	assert.Equal(t, true, validSignature, "Expected a valid signature")
	var updateManifest types.UpdateManifest
	err = json.Unmarshal([]byte(body), &updateManifest)
	if err != nil {
		t.Errorf("Error parsing json body: %v", err)
	}
	assert.Equal(t, "1990-01-01T00:00:00.000Z", updateManifest.CreatedAt, "Expected a specific created at date")
	assert.Equal(t, "1", updateManifest.RunTimeVersion, "Expected a specific runtime version")
	assert.Equal(t, json.RawMessage("{\"branch\":\"branch-2\"}"), updateManifest.Metadata, "Expected branch in metadata")
	assert.Equal(t, "{\"id\":\"68e096e2-a619-9d56-7f7c-89f97bc27312\",\"createdAt\":\"1990-01-01T00:00:00.000Z\",\"runtimeVersion\":\"1\",\"metadata\":{\"branch\":\"branch-2\"},\"assets\":[{\"hash\":\"JCcs2u_4LMX6zazNmCpvBbYMRQRwS7-UwZpjiGWYgLs\",\"key\":\"4f1cb2cac2370cd5050681232e8575a8\",\"fileExtension\":\".png\",\"contentType\":\"application/javascript\",\"url\":\"http://localhost:3000/assets?asset=assets%2F4f1cb2cac2370cd5050681232e8575a8\\u0026branch=branch-2\\u0026platform=ios\\u0026runtimeVersion=1\\u0026updateId=1737455526\"}],\"launchAsset\":{\"hash\":\"vH93RoNbdzk_2emr38L0ZVYJVBTPcspX5-5DXLUkiQ8\",\"key\":\"e44a25e2b1df198470a04adc1dd82e4e\",\"fileExtension\":\".bundle\",\"contentType\":\"\",\"url\":\"http://localhost:3000/assets?asset=_expo%2Fstatic%2Fjs%2Fios%2FAppEntry-546b83fc2035b34c5f2dbd9bb04a2478.hbc\\u0026branch=branch-2\\u0026platform=ios\\u0026runtimeVersion=1\\u0026updateId=1737455526\"},\"extra\":{\"expoClient\":{\"name\":\"expo-updates-client\",\"slug\":\"expo-updates-client\",\"owner\":\"anonymous\",\"version\":\"1.0.0\",\"orientation\":\"portrait\",\"icon\":\"./assets/icon.png\",\"splash\":{\"image\":\"./assets/splash.png\",\"resizeMode\":\"contain\",\"backgroundColor\":\"#ffffff\"},\"runtimeVersion\":\"1\",\"updates\":{\"url\":\"http://localhost:3000/api/manifest\",\"enabled\":true,\"fallbackToCacheTimeout\":30000},\"assetBundlePatterns\":[\"**/*\"],\"ios\":{\"supportsTablet\":true,\"bundleIdentifier\":\"com.test.expo-updates-client\"},\"android\":{\"adaptiveIcon\":{\"foregroundImage\":\"./assets/adaptive-icon.png\",\"backgroundColor\":\"#FFFFFF\"},\"package\":\"com.test.expoupdatesclient\"},\"web\":{\"favicon\":\"./assets/favicon.png\"},\"plugins\":[[\"expo-build-properties\",{\"android\":{\"usesCleartextTraffic\":true},\"ios\":{}}]],\"sdkVersion\":\"52.0.0\",\"platforms\":[\"ios\",\"android\"],\"currentFullName\":\"@anonymous/expo-updates-client\",\"originalFullName\":\"@anonymous/expo-updates-client\"},\"branch\":\"branch-2\"}}", body)
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
	r.Header.Add("expo-app-id", "test-app-id")
	testContainer().ExpoProtocolHandler.HandleManifest(w, r)
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
	validSignature := ValidateSignatureHeader("test-app-id", signature, body)
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
	lastUpdateKey := update.ComputeLastUpdateCacheKey("test-app-id", "branch-1", "1", "android")
	assert.Equal(t, "", cache.Get(lastUpdateKey), "lastUpdate cache should be empty before prewarm")

	// Run PreWarm synchronously (not as goroutine) for testing
	services.PreWarmManifestCache(testUpdateService(), "test-app-id", "branch-1", "1", "android")

	// Verify lastUpdate cache was populated
	lastUpdateCached := cache.Get(lastUpdateKey)
	assert.NotEqual(t, "", lastUpdateCached, "lastUpdate cache should be populated after prewarm")

	// Verify metadata cache was populated
	var cachedUpdate types.Update
	err := json.Unmarshal([]byte(lastUpdateCached), &cachedUpdate)
	assert.NoError(t, err)
	metadataKey := update.ComputeMetadataCacheKey("test-app-id", "branch-1", "1", cachedUpdate.UpdateId)
	assert.NotEqual(t, "", cache.Get(metadataKey), "metadata cache should be populated after prewarm")

	// Verify manifest cache was populated
	manifestKey := update.ComputeUpdateManifestCacheKey("test-app-id", "branch-1", "1", cachedUpdate.UpdateId, "android")
	assert.NotEqual(t, "", cache.Get(manifestKey), "manifest cache should be populated after prewarm")
}

// checkInRequest builds a poll carrying the headers a real client sends,
// including the device id and the update-health headers a check-in would
// record.
func checkInRequest(channelName string, runtimeVersion string) *http.Request {
	r := httptest.NewRequest("GET", "http://localhost:3000/manifest", nil)
	r.Header.Add("expo-platform", "ios")
	r.Header.Add("expo-runtime-version", runtimeVersion)
	r.Header.Add("expo-protocol-version", "1")
	r.Header.Add("expo-expect-signature", "true")
	r.Header.Add("expo-channel-name", channelName)
	r.Header.Add("expo-app-id", "test-app-id")
	r.Header.Add("EAS-Client-ID", "d8a43b52-9b1b-4b6a-9e57-52a2ce271244")
	r.Header.Add("expo-current-update-id", "2fc0ffee-0000-4000-8000-00000000cafe")
	r.Header.Add("Expo-Recent-Failed-Update-Ids", `"3fc0ffee-0000-4000-8000-00000000cafe"`)
	return r
}

// serveWithCheckInRecorder serves one manifest request through a handler whose
// check-in seam records instead of writing, and returns what the registry
// would have been asked to store.
func serveWithCheckInRecorder(r *http.Request) (*httptest.ResponseRecorder, []handlers.DeviceCheckIn) {
	var recorded []handlers.DeviceCheckIn
	handler := testContainer().ExpoProtocolHandler
	handler.SetOnDeviceCheckIn(func(_ context.Context, checkIn handlers.DeviceCheckIn) {
		recorded = append(recorded, checkIn)
	})
	w := httptest.NewRecorder()
	handler.HandleManifest(w, r)
	return w, recorded
}

// The device registry is a durable table written from an unauthenticated
// endpoint, so a poll must only count as a check-in once the request has
// resolved. A rejected poll is not evidence that a device exists, and letting
// it register would make the table grow on requests the server itself refuses
// to answer.
func TestManifestChecksInOnlyAfterResolution(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	// The one exception, and it writes nothing durable: the crash detail is
	// sent by the client exactly once, and a TRANSIENT resolution failure is
	// correlated with the incident it documents. The recorder holds it in
	// memory for the next poll that resolves.
	t.Run("a transiently refused poll still hands over its crash detail", func(t *testing.T) {
		httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
			func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("operationName") == "FetchExpoChannelMapping" {
					return httpmock.NewStringResponse(http.StatusInternalServerError, ""), nil
				}
				return MockExpoAccountResponse(map[string]interface{}{
					"id": "test_id", "username": "test_username", "email": "test_email",
				})
			})
		r := checkInRequest("staging", "1")
		r.Header.Set("expo-fatal-error", "TypeError: undefined is not a function")

		w, recorded := serveWithCheckInRecorder(r)
		assert.Equal(t, 500, w.Code)
		assert.Len(t, recorded, 1)
		assert.True(t, recorded[0].Rejected, "the recorder must know this poll was refused")
		assert.Equal(t, "TypeError: undefined is not a function", recorded[0].FatalError)
		// Nothing else travels for a device that is not being registered.
		assert.Equal(t, "", recorded[0].CurrentUpdateID)
	})

	// A 4xx says the app or the channel does not exist, and that answer will
	// not change on its own: there is no later poll to re-attach a detail to.
	// Stashing there would let an unknown app id put an entry in the cache for
	// an hour, once per request, through the very mechanism meant to avoid
	// writing anything.
	t.Run("a permanently refused poll hands over nothing at all", func(t *testing.T) {
		mockWorkingExpoResponse("staging")
		r := checkInRequest("staging", "1")
		r.Header.Set("expo-app-id", "no-such-app")
		r.Header.Set("expo-fatal-error", "TypeError: undefined is not a function")

		w, recorded := serveWithCheckInRecorder(r)
		assert.Equal(t, 404, w.Code)
		assert.Empty(t, recorded)
	})

	t.Run("a channel that maps to no branch registers nothing", func(t *testing.T) {
		httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
			func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("operationName") == "FetchExpoChannelMapping" {
					return MockExpoChannelMapping(nil, map[string]interface{}{
						"id":   "bad_channel_id",
						"name": "bad_channel",
						"branchMapping": StringifyBranchMapping(map[string]interface{}{
							"version": 0,
							"data":    []map[string]interface{}{},
						}),
					})
				}
				return MockExpoAccountResponse(map[string]interface{}{
					"id": "test_id", "username": "test_username", "email": "test_email",
				})
			})

		w, recorded := serveWithCheckInRecorder(checkInRequest("bad_channel", "1"))
		assert.Equal(t, 404, w.Code)
		assert.Empty(t, recorded, "a poll the server rejects must leave no device behind")
	})

	t.Run("an unknown app id registers nothing", func(t *testing.T) {
		mockWorkingExpoResponse("staging")
		r := checkInRequest("staging", "1")
		r.Header.Set("expo-app-id", "no-such-app")

		w, recorded := serveWithCheckInRecorder(r)
		assert.Equal(t, 404, w.Code)
		assert.Empty(t, recorded)
	})

	// Resolving to no update is a legitimate outcome, not a rejection: the app,
	// the channel and the branch were all real. A device ahead of every
	// published update, or out of a rollout bucket, is as alive as any other
	// and must keep its place in the registry.
	t.Run("a poll that resolves to no update still checks in", func(t *testing.T) {
		mockWorkingExpoResponse("staging")

		w, recorded := serveWithCheckInRecorder(checkInRequest("staging", "nop"))
		assert.Equal(t, 200, w.Code)
		assert.Len(t, recorded, 1)
		assert.Equal(t, "test-app-id", recorded[0].AppID)
		assert.Equal(t, "d8a43b52-9b1b-4b6a-9e57-52a2ce271244", recorded[0].EASClientID)
		// The health signals the same headers carried must survive the move.
		assert.Equal(t, "2fc0ffee-0000-4000-8000-00000000cafe", recorded[0].CurrentUpdateID)
		assert.Equal(t, `"3fc0ffee-0000-4000-8000-00000000cafe"`, recorded[0].FailedUpdateIDsRaw)
	})

	t.Run("a served update checks in", func(t *testing.T) {
		mockWorkingExpoResponse("staging")
		r := checkInRequest("staging", "1")
		// The fixture publishes runtime 1 for android only, so this is the
		// subtest that actually walks the served-manifest branch.
		r.Header.Set("expo-platform", "android")

		w, recorded := serveWithCheckInRecorder(r)
		assert.Equal(t, 200, w.Code)
		parts, err := ParseMultipartMixedResponse(w.Header().Get("Content-Type"), w.Body.Bytes())
		assert.NoError(t, err)
		assert.Len(t, parts, 1)
		assert.Equal(t, "manifest", parts[0].Name, "this poll must be answered with an update, not a directive")
		assert.Len(t, recorded, 1)
	})
}

func TestManifestSignatureCache(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	signedManifestRequest := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "http://localhost:3000/manifest", nil)
		r.Header.Add("expo-platform", "ios")
		r.Header.Add("expo-runtime-version", "nop")
		r.Header.Add("expo-protocol-version", "1")
		r.Header.Add("expo-expect-signature", "true")
		r.Header.Add("expo-channel-name", "staging")
		r.Header.Add("expo-app-id", "test-app-id")
		mockWorkingExpoResponse("staging")
		testContainer().ExpoProtocolHandler.HandleManifest(w, r)
		return w
	}

	first := signedManifestRequest()
	assert.Equal(t, 200, first.Code)
	firstParts, err := ParseMultipartMixedResponse(first.Header().Get("Content-Type"), first.Body.Bytes())
	assert.NoError(t, err)
	assert.Len(t, firstParts, 1)
	firstSignature := firstParts[0].Headers["Expo-Signature"]
	assert.True(t, ValidateSignatureHeader("test-app-id", firstSignature, firstParts[0].Body))

	projectRoot, err := findProjectRoot()
	assert.NoError(t, err)
	privateKeyPEM, err := os.ReadFile(filepath.Join(projectRoot, "/test/keys/private-key-test.pem"))
	assert.NoError(t, err)
	keyFingerprint, err := crypto2.CreateHash(privateKeyPEM, "sha256", "hex")
	assert.NoError(t, err)
	contentHash, err := crypto2.CreateHash([]byte(firstParts[0].Body), "sha256", "hex")
	assert.NoError(t, err)
	cachedSignature := cache2.GetCache().Get("manifest-signature:" + version.Version + ":test-app-id:" + keyFingerprint + ":" + contentHash)
	assert.NotEqual(t, "", cachedSignature, "the first signed response must store its signature in the cache")
	assert.True(t, strings.Contains(firstSignature, cachedSignature), "the served signature and the cached one must match")

	second := signedManifestRequest()
	assert.Equal(t, 200, second.Code)
	secondParts, err := ParseMultipartMixedResponse(second.Header().Get("Content-Type"), second.Body.Bytes())
	assert.NoError(t, err)
	assert.Equal(t, firstSignature, secondParts[0].Headers["Expo-Signature"])
	assert.True(t, ValidateSignatureHeader("test-app-id", secondParts[0].Headers["Expo-Signature"], secondParts[0].Body))

	// Once the app-config entry expires (simulated by the Delete), a rotated
	// key must miss the signature cache: with the new key unreadable, signing
	// has to fail rather than serve the signature made with the old key.
	os.Setenv("PRIVATE_LOCAL_EXPO_KEY_PATH", filepath.Join(projectRoot, "/test/keys/rotated-missing.pem"))
	config.ResetAppsForTest()
	if err := config.LoadAppsFromFlatEnv(); err != nil {
		t.Fatalf("LoadAppsFromFlatEnv: %v", err)
	}
	defer SetValidConfiguration()
	cache2.GetCache().Delete("app-config:" + version.Version + ":test-app-id")
	third := signedManifestRequest()
	assert.Equal(t, 500, third.Code)
	assert.Equal(t, "Error signing content\n", third.Body.String())
}
