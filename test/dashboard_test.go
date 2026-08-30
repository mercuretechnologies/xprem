package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"xprem/config"
	"xprem/internal/cdn"
	infrastructure "xprem/internal/router"
	"xprem/internal/services"
	"xprem/internal/types"

	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/assert"
)

func TestLoginDashboardNotEnabled(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("USE_DASHBOARD", "false")
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/auth/login", nil)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusNotFound, respRec.Code)

}

func TestRootRedirectsToDashboard(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	t.Setenv("DASHBOARD_ROOT_REDIRECT", "true")
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusFound, respRec.Code)
	assert.Equal(t, "/dashboard/", respRec.Header().Get("Location"))
}

func TestRootNotFoundByDefault(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	t.Setenv("DASHBOARD_ROOT_REDIRECT", "")
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusNotFound, respRec.Code)
}

func TestRootNotFoundWhenDashboardDisabled(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("USE_DASHBOARD", "false")
	t.Setenv("DASHBOARD_ROOT_REDIRECT", "true")
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusNotFound, respRec.Code)
}

func TestRootPostNotRedirected(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	t.Setenv("DASHBOARD_ROOT_REDIRECT", "true")
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/", nil)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, respRec.Code)
}

func loginRequest(email string, password string) *http.Request {
	formData := url.Values{}
	formData.Set("email", email)
	formData.Set("password", password)
	req, _ := http.NewRequest("POST", "/auth/login", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestLoginInvalidPassword(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	router.ServeHTTP(respRec, loginRequest("admin@xprem.dev", "wrongpassword"))
	assert.Equal(t, http.StatusUnauthorized, respRec.Code)
}

func TestLoginInvalidEmail(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	router.ServeHTTP(respRec, loginRequest("someone-else@xprem.dev", "admin"))
	assert.Equal(t, http.StatusUnauthorized, respRec.Code)
}

func TestShouldRejectLoginIfAdminPasswordNotSet(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("ADMIN_PASSWORD", "")
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	router.ServeHTTP(respRec, loginRequest("admin@xprem.dev", "admin"))
	assert.Equal(t, http.StatusUnauthorized, respRec.Code)
}

// A missing ADMIN_EMAIL is an operator misconfiguration: stateless-mode login
// must answer with the explicit "set ADMIN_EMAIL" instruction, not a 401.
func TestShouldExplainLoginIfAdminEmailNotSet(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("ADMIN_EMAIL", "")
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	router.ServeHTTP(respRec, loginRequest("admin@xprem.dev", "admin"))
	assert.Equal(t, http.StatusInternalServerError, respRec.Code)
	assert.Contains(t, respRec.Body.String(), "ADMIN_EMAIL is not set")
}

func TestLoginValidCredentials(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	// Email matching is case-insensitive.
	router.ServeHTTP(respRec, loginRequest("Admin@XPrem.dev", "admin"))
	assert.Equal(t, http.StatusOK, respRec.Code)
	// Retrieve token & refreshToken from response
	body := respRec.Body.String()

	var response services.DashboardSession
	err := json.Unmarshal([]byte(body), &response)
	assert.Nil(t, err)
	assert.NotEmpty(t, response.Token)
	assert.NotEmpty(t, response.RefreshToken)
}

func login() services.DashboardSession {
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	router.ServeHTTP(respRec, loginRequest("admin@xprem.dev", "admin"))
	body := respRec.Body.String()
	var response services.DashboardSession
	_ = json.Unmarshal([]byte(body), &response)
	return response
}

// In stateless mode /api/me synthesizes the account from ADMIN_EMAIL: no id,
// always admin.
func TestGetMeStateless(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+login().Token)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusOK, respRec.Code)
	assert.Equal(t, `{"id":"","email":"admin@xprem.dev","isAdmin":true,"enabled":true}`, strings.TrimSpace(respRec.Body.String()))
}

// User management is a control-plane feature: in stateless mode the routes
// exist but answer an explicit 400, and the single env account is admin so the
// admin gate itself passes.
func TestUsersRoutesRejectedInStatelessMode(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/users", nil)
	req.Header.Set("Authorization", "Bearer "+login().Token)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusBadRequest, respRec.Code)
	assert.Contains(t, respRec.Body.String(), "stateless mode")
}

func TestRefreshToken(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	formData := url.Values{}
	formData.Set("refreshToken", login().RefreshToken)
	req, _ := http.NewRequest("POST", "/auth/refreshToken", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusOK, respRec.Code)
	body := respRec.Body.String()
	var response services.DashboardSession
	err := json.Unmarshal([]byte(body), &response)
	assert.Nil(t, err)
	assert.NotEmpty(t, response.Token)
	assert.NotEmpty(t, response.RefreshToken)
}

func TestSettings(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+login().Token)
	router.ServeHTTP(respRec, req)

	assert.Equal(t, http.StatusOK, respRec.Code)

	projectRoot, err := os.Getwd()
	assert.Nil(t, err)

	responseBody := strings.TrimSpace(respRec.Body.String())

	responseBody = strings.ReplaceAll(responseBody, projectRoot+"/test-updates", "{PROJECT_ROOT}/test/test-updates")
	responseBody = strings.ReplaceAll(responseBody, projectRoot+"/keys/public-key-test.pem", "{PROJECT_ROOT}/test/keys/public-key-test.pem")
	responseBody = strings.ReplaceAll(responseBody, projectRoot+"/keys/private-key-test.pem", "{PROJECT_ROOT}/test/keys/private-key-test.pem")

	expectedSnapshot := `{"BASE_URL":"http://localhost:3000","SERVER_VERSION":"development","CONTROL_PLANE_ENABLED":false,"CACHE_MODE":"","REDIS_HOST":"","REDIS_PORT":"","REDIS_SENTINEL_ADDRS":"","REDIS_SENTINEL_MASTER_NAME":"","STORAGE_MODE":"local","S3_BUCKET_NAME":"","CDN_BASE_URL":"","GCS_BUCKET_NAME":"","AZURE_BLOB_CONTAINER_NAME":"","AZURE_STORAGE_ACCOUNT_NAME":"","LOCAL_BUCKET_BASE_PATH":"{PROJECT_ROOT}/test/test-updates","AWS_REGION":"eu-west-3","AWS_BASE_ENDPOINT":"","AWS_S3_FORCE_PATH_STYLE":"","AWS_ACCESS_KEY_ID":"***","CLOUDFRONT_DOMAIN":"","CLOUDFRONT_KEY_PAIR_ID":"***","PRIVATE_CLOUDFRONT_KEY_B64":"***","AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID":"","PRIVATE_CLOUDFRONT_KEY_PATH":"","PROMETHEUS_ENABLED":"","CDN_TYPE":"","EXPO_ACCOUNT_USERNAME":"","SSO_ENABLED":false,"APPS":[{"id":"test-app-id"}]}`

	assert.Equal(t, expectedSnapshot, responseBody)
}

// Operators migrating from S3_CDN_PREFIX must see the resolved generic CDN in
// the dashboard: CDN_TYPE reports "generic" and CDN_BASE_URL carries the value
// even when it comes from the deprecated variable.
func TestSettingsReportsGenericCDNFromLegacyVariable(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "s3")
	os.Setenv("S3_BUCKET_NAME", "test-bucket")
	os.Setenv("S3_CDN_PREFIX", "https://cdn.example.com")
	defer func() {
		os.Unsetenv("STORAGE_MODE")
		os.Unsetenv("S3_BUCKET_NAME")
		os.Unsetenv("S3_CDN_PREFIX")
	}()
	cdn.ResetCDNInstance()

	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+login().Token)
	router.ServeHTTP(respRec, req)

	assert.Equal(t, http.StatusOK, respRec.Code)
	var payload map[string]any
	assert.Nil(t, json.Unmarshal(respRec.Body.Bytes(), &payload))
	assert.Equal(t, "generic", payload["CDN_TYPE"])
	assert.Equal(t, "https://cdn.example.com", payload["CDN_BASE_URL"])
}

// In stateless mode the flat env carries no display name, so the dashboard
// would label everything with the raw EXPO_APP_ID. The bucket store resolves
// the name from Expo (best-effort); TestSettings covers the failure path,
// where the name stays empty and clients fall back to the id.
func TestGetAppsStatelessResolvesNameFromExpo(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("operationName") == "FetchExpoAppName" {
				return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
					"data": map[string]interface{}{
						"app": map[string]interface{}{
							"byId": map[string]interface{}{
								"id":   "test-app-id",
								"name": "My Expo App",
							},
						},
					},
				})
			}
			return httpmock.NewStringResponse(404, "Unknown operation"), nil
		})
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/apps", nil)
	req.Header.Set("Authorization", "Bearer "+login().Token)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusOK, respRec.Code)
	assert.Equal(t, `[{"id":"test-app-id","name":"My Expo App"}]`, strings.TrimSpace(respRec.Body.String()))
}

func TestSettingsWithoutAuth(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/settings", nil)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusUnauthorized, respRec.Code)
}

func TestBranches(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			return MockExpoBranchesMappingResponse([]map[string]interface{}{{"id": "branch-1", "name": "branch-1"}, {"id": "branch-2", "name": "branch-2"}}, []map[string]interface{}{{"id": "staging", "name": "staging", "branchMapping": "{\"data\":[{\"branchId\":\"branch-1\",\"branchMappingLogic\":\"true\"}],\"version\":0}"}})
		})
	req, _ := http.NewRequest("GET", "/api/apps/test-app-id/branches", nil)
	req.Header.Set("Authorization", "Bearer "+login().Token)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusOK, respRec.Code)

	var response []types.BranchMapping
	err := json.Unmarshal(respRec.Body.Bytes(), &response)
	assert.Nil(t, err)
	assert.Equal(t, `[{"branchName":"branch-1","branchId":"branch-1","releaseChannel":"staging","createdAt":null,"protected":false},{"branchName":"branch-2","branchId":"branch-2","releaseChannel":null,"createdAt":null,"protected":false},{"branchName":"branch-3","branchId":null,"releaseChannel":null,"createdAt":null,"protected":false},{"branchName":"branch-4","branchId":null,"releaseChannel":null,"createdAt":null,"protected":false},{"branchName":"branch-legacy","branchId":null,"releaseChannel":null,"createdAt":null,"protected":false}]`, strings.TrimSpace(respRec.Body.String()))
}

func TestBranchesWithoutAuth(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/apps/test-app-id/branches", nil)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusUnauthorized, respRec.Code)
}

// The dashboard /api/apps/{APP_ID}/... routes must run AppResolverMiddleware
// so unknown app ids return 404, without it handlers fall through to
// bucket lookups and can answer 200 with [] for a nonexistent app.
func TestDashboardUnknownAppIdReturns404(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/apps/does-not-exist/branches", nil)
	req.Header.Set("Authorization", "Bearer "+login().Token)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusNotFound, respRec.Code)
}

// AuthMiddleware must reject a syntactically valid but cryptographically bogus
// JWT. The "no header" path is covered by TestSettingsWithoutAuth et al; this
// closes the gap where a malicious caller sends garbage in the Bearer slot.
func TestDashboardRejectsInvalidBearerToken(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer not.a.real.jwt")
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusUnauthorized, respRec.Code)
}

// Use-Cli-Auth relays the caller's Expo credentials on app-scoped routes;
// on app-agnostic routes (here /api/settings) there is no APP_ID to validate
// against, so the middleware must short-circuit with 401 instead of falling
// through to an Expo call it cannot make.
func TestDashboardUseExpoAuthRejectedOnAppAgnosticRoute(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Use-Cli-Auth", "true")
	req.Header.Set("Authorization", "Bearer expo_test_token")
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusUnauthorized, respRec.Code)
}

// Use-Cli-Auth with a bearer the Expo API rejects must surface as a 401
// from the middleware. Exercises the ValidateExpoAuth failure branch for
// the dashboard-relayed Expo session path.
func TestDashboardUseExpoAuthRejectsInvalidExpoToken(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			// Only FetchExpoUserAccountInformations runs before the reject -
			// branch mapping never fires because auth short-circuits first.
			if req.Header.Get("operationName") == "FetchExpoUserAccountInformations" {
				if req.Header.Get("Authorization") == "Bearer bogus_expo_token" {
					return httpmock.NewStringResponse(http.StatusUnauthorized, `{"errors":[{"message":"Unauthorized"}]}`), nil
				}
				return MockExpoAccountResponse(map[string]interface{}{
					"id":       "123",
					"username": "test_username",
					"email":    "test@example.com",
				})
			}
			return httpmock.NewStringResponse(404, "Unknown operation"), nil
		})

	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/apps/test-app-id/branches", nil)
	req.Header.Set("Use-Cli-Auth", "true")
	req.Header.Set("Authorization", "Bearer bogus_expo_token")
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusUnauthorized, respRec.Code)
}

// TestDashboardUseExpoAuthCrossAppAttackRejected, the promise of
// Use-Cli-Auth is that a caller can only reach an app whose stored
// EXPO_ACCESS_TOKEN resolves to the same Expo user as their session.
// If two tenants coexist on the server, a caller with a valid Expo
// token for tenant A must NOT be able to read tenant B via
// /api/apps/{B}/... The middleware enforces this by calling Expo with
// BOTH tokens and comparing the returned usernames, a mismatch is
// a 401, no 500, no fall-through to the handler.
func TestDashboardUseExpoAuthCrossAppAttackRejected(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	// Two apps owned by different Expo users, seeded after the router build
	// below (see note there).
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("operationName") != "FetchExpoUserAccountInformations" {
				return httpmock.NewStringResponse(404, "Unknown operation"), nil
			}
			// Map each bearer to a distinct username so usernames differ
			// across apps, that is what makes the match check fail.
			switch req.Header.Get("Authorization") {
			case "Bearer token-app-1", "Bearer expo_session_of_user_1":
				return MockExpoAccountResponse(map[string]interface{}{"id": "1", "username": "user-1"})
			case "Bearer token-app-2":
				return MockExpoAccountResponse(map[string]interface{}{"id": "2", "username": "user-2"})
			}
			return httpmock.NewStringResponse(http.StatusUnauthorized, `{"errors":[]}`), nil
		})

	router := infrastructure.NewRouter(testContainer())
	// Seed the two tenants AFTER the container build: InitDependencies re-runs
	// config.LoadAppsFromFlatEnv (flat-env single app), which would otherwise clobber this
	// injection. The bucket app store reads the registry live, so the resolver
	// middleware sees app-1/app-2 at request time. app-1's token resolves to
	// "user-1"; app-2's to "user-2", distinct usernames are what make the
	// cross-app match check fail.
	config.SetAppsForTest([]config.AppConfig{
		{Id: "app-1", AccessToken: "token-app-1", Keys: config.KeysConfig{Mode: config.KeysModeLocal, PublicPath: "/a", PrivatePath: "/b"}},
		{Id: "app-2", AccessToken: "token-app-2", Keys: config.KeysConfig{Mode: config.KeysModeLocal, PublicPath: "/a", PrivatePath: "/b"}},
	})
	respRec := httptest.NewRecorder()
	// Caller holds an Expo session valid for user-1, but hits /api/apps/app-2/…
	req, _ := http.NewRequest("GET", "/api/apps/app-2/branches", nil)
	req.Header.Set("Use-Cli-Auth", "true")
	req.Header.Set("Authorization", "Bearer expo_session_of_user_1")
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusUnauthorized, respRec.Code)
}

// Use-Cli-Auth happy path: caller's Expo token resolves to the same username
// as the app's EXPO_ACCESS_TOKEN (ValidateExpoAuth's match check) so the
// middleware lets the request through to the handler.
//
// On runtimeVersions rather than on branches, because the route has to be one
// the CLI is allowed to reach: only the two routes eoas calls before publishing
// declare AnyViewerOrToken, everything else in routes_app.go refuses a
// publishing credential outright.
func TestDashboardUseExpoAuthHappyPath(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			op := req.Header.Get("operationName")
			if op == "FetchExpoUserAccountInformations" {
				// Both the caller's token and the app's EXPO_ACCESS_TOKEN
				// resolve to "test_username", that's what makes the
				// match in ValidateExpoAuth succeed.
				return MockExpoAccountResponse(map[string]interface{}{
					"id":       "123",
					"username": "test_username",
					"email":    "test@example.com",
				})
			}
			// Handler-level call once auth passes.
			return MockExpoBranchesMappingResponse(
				[]map[string]interface{}{{"id": "branch-1", "name": "branch-1"}},
				[]map[string]interface{}{{"id": "staging", "name": "staging", "branchMapping": "{\"data\":[{\"branchId\":\"branch-1\",\"branchMappingLogic\":\"true\"}],\"version\":0}"}},
			)
		})

	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/apps/test-app-id/branch/branch-1/runtimeVersions", nil)
	req.Header.Set("Use-Cli-Auth", "true")
	req.Header.Set("Authorization", "Bearer expo_test_token")
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusOK, respRec.Code)
}

func TestRuntimeVersionsWithoutAuth(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/apps/test-app-id/branch/branch-1/runtimeVersions", nil)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusUnauthorized, respRec.Code)
}

func TestRuntimeVersions(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			return MockExpoBranchesMappingResponse([]map[string]interface{}{{"id": "branch-1", "name": "branch-1"}, {"id": "branch-2", "name": "branch-2"}}, []map[string]interface{}{{"id": "staging", "name": "staging", "branchMapping": "{\"data\":[{\"branchId\":\"branch-1\",\"branchMappingLogic\":\"true\"}],\"version\":0}"}})
		})
	req, _ := http.NewRequest("GET", "/api/apps/test-app-id/branch/branch-1/runtimeVersions", nil)
	req.Header.Set("Authorization", "Bearer "+login().Token)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusOK, respRec.Code)
	var response []types.RuntimeVersionWithStats
	err := json.Unmarshal(respRec.Body.Bytes(), &response)
	assert.Nil(t, err)
	assert.Equal(t, "[{\"runtimeVersion\":\"1\",\"lastUpdatedAt\":\"1970-01-20T09:02:50Z\",\"createdAt\":\"1970-01-20T09:02:50Z\",\"numberOfUpdates\":1}]", strings.TrimSpace(respRec.Body.String()))
}

func TestRuntimeVersionsOnlyCountsValidUpdates(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			return MockExpoBranchesMappingResponse([]map[string]interface{}{{"id": "branch-1", "name": "branch-1"}, {"id": "branch-2", "name": "branch-2"}}, []map[string]interface{}{{"id": "staging", "name": "staging", "branchMapping": "{\"data\":[{\"branchId\":\"branch-1\",\"branchMappingLogic\":\"true\"}],\"version\":0}"}})
		})
	req, _ := http.NewRequest("GET", "/api/apps/test-app-id/branch/branch-4/runtimeVersions", nil)
	req.Header.Set("Authorization", "Bearer "+login().Token)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusOK, respRec.Code)
	var response []types.RuntimeVersionWithStats
	err := json.Unmarshal(respRec.Body.Bytes(), &response)
	assert.Nil(t, err)
	assert.Equal(t, "[{\"runtimeVersion\":\"1\",\"lastUpdatedAt\":\"1970-01-20T09:02:50Z\",\"createdAt\":\"1970-01-20T09:02:50Z\",\"numberOfUpdates\":1}]", strings.TrimSpace(respRec.Body.String()))
}

func TestUpdatesWithoutAuth(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/apps/test-app-id/branch/branch-1/runtimeVersion/1/updates", nil)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusUnauthorized, respRec.Code)
}

func TestUpdatesRejectsInvalidPagination(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	token := login().Token

	for _, query := range []string{"cursor=invalid", "cursor=0", "limit=0", "limit=101"} {
		respRec := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/apps/test-app-id/branch/branch-1/runtimeVersion/1/updates?"+query, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(respRec, req)
		assert.Equal(t, http.StatusBadRequest, respRec.Code, query)
	}
}

func TestUpdatesRegularBranch1(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			return MockExpoBranchesMappingResponse([]map[string]interface{}{{"id": "branch-1", "name": "branch-1"}, {"id": "branch-2", "name": "branch-2"}}, []map[string]interface{}{{"id": "staging", "name": "staging", "branchMapping": "{\"data\":[{\"branchId\":\"branch-1\",\"branchMappingLogic\":\"true\"}],\"version\":0}"}})
		})
	req, _ := http.NewRequest("GET", "/api/apps/test-app-id/branch/branch-1/runtimeVersion/1/updates", nil)
	req.Header.Set("Authorization", "Bearer "+login().Token)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusOK, respRec.Code)
	assert.Equal(t, "{\"items\":[{\"updateUUID\":\"04b793a0-b6ab-fd4f-308c-b91d812adec2\",\"updateId\":\"1674170951\",\"createdAt\":\"1970-01-20T09:02:50Z\",\"commitHash\":\"1674170951\",\"platform\":\"android\"}],\"nextCursor\":null}", strings.TrimSpace(respRec.Body.String()))
}

func TestUpdatesMultiBranch2(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			return MockExpoBranchesMappingResponse([]map[string]interface{}{{"id": "branch-1", "name": "branch-1"}, {"id": "branch-2", "name": "branch-2"}}, []map[string]interface{}{{"id": "staging", "name": "staging", "branchMapping": "{\"data\":[{\"branchId\":\"branch-1\",\"branchMappingLogic\":\"true\"}],\"version\":0}"}})
		})
	req, _ := http.NewRequest("GET", "/api/apps/test-app-id/branch/branch-2/runtimeVersion/1/updates", nil)
	req.Header.Set("Authorization", "Bearer "+login().Token)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusOK, respRec.Code)
	assert.Equal(t, "{\"items\":[{\"updateUUID\":\"68e096e2-a619-9d56-7f7c-89f97bc27312\",\"updateId\":\"1737455526\",\"createdAt\":\"1970-01-21T02:37:35Z\",\"commitHash\":\"\",\"platform\":\"ios\"},{\"updateUUID\":\"fdc14544-9e15-732f-cd9c-e3e26c55cbea\",\"updateId\":\"1674170951\",\"createdAt\":\"1970-01-20T09:02:50Z\",\"commitHash\":\"\",\"platform\":\"android\"},{\"updateUUID\":\"Rollback to embedded\",\"updateId\":\"1666629141\",\"createdAt\":\"1970-01-20T06:57:09Z\",\"commitHash\":\"1674170951\",\"platform\":\"ios\"},{\"updateUUID\":\"d100f19f-e0be-45c4-212a-27d1f067552b\",\"updateId\":\"1666629107\",\"createdAt\":\"1970-01-20T06:57:09Z\",\"commitHash\":\"1674170951\",\"platform\":\"android\"},{\"updateUUID\":\"Rollback to embedded\",\"updateId\":\"1666304169\",\"createdAt\":\"1970-01-20T06:51:44Z\",\"commitHash\":\"1674170951\",\"platform\":\"ios\"}],\"nextCursor\":null}", strings.TrimSpace(respRec.Body.String()))
}

func TestUpdatesCursorPagination(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			return MockExpoBranchesMappingResponse([]map[string]interface{}{{"id": "branch-1", "name": "branch-1"}, {"id": "branch-2", "name": "branch-2"}}, []map[string]interface{}{{"id": "staging", "name": "staging", "branchMapping": "{\"data\":[{\"branchId\":\"branch-1\",\"branchMappingLogic\":\"true\"}],\"version\":0}"}})
		})
	token := login().Token

	fetchPage := func(path string) types.UpdatesPage {
		respRec := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(respRec, req)
		assert.Equal(t, http.StatusOK, respRec.Code)
		var page types.UpdatesPage
		assert.NoError(t, json.Unmarshal(respRec.Body.Bytes(), &page))
		return page
	}

	first := fetchPage("/api/apps/test-app-id/branch/branch-2/runtimeVersion/1/updates?limit=2")
	assert.Equal(t, []string{"1737455526", "1674170951"}, []string{first.Items[0].UpdateId, first.Items[1].UpdateId})
	assert.Equal(t, "1674170951", *first.NextCursor)

	second := fetchPage("/api/apps/test-app-id/branch/branch-2/runtimeVersion/1/updates?limit=2&cursor=" + *first.NextCursor)
	assert.Equal(t, []string{"1666629141", "1666629107"}, []string{second.Items[0].UpdateId, second.Items[1].UpdateId})
	assert.Equal(t, "1666629107", *second.NextCursor)

	last := fetchPage("/api/apps/test-app-id/branch/branch-2/runtimeVersion/1/updates?limit=2&cursor=" + *second.NextCursor)
	assert.Equal(t, []string{"1666304169"}, []string{last.Items[0].UpdateId})
	assert.Nil(t, last.NextCursor)
}

func TestUpdatesSomeNotValidBranch4(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			return MockExpoBranchesMappingResponse([]map[string]interface{}{{"id": "branch-1", "name": "branch-1"}, {"id": "branch-2", "name": "branch-2"}}, []map[string]interface{}{{"id": "staging", "name": "staging", "branchMapping": "{\"data\":[{\"branchId\":\"branch-1\",\"branchMappingLogic\":\"true\"}],\"version\":0}"}})
		})
	req, _ := http.NewRequest("GET", "/api/apps/test-app-id/branch/branch-4/runtimeVersion/1/updates", nil)
	req.Header.Set("Authorization", "Bearer "+login().Token)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusOK, respRec.Code)
	assert.Equal(t, "{\"items\":[{\"updateUUID\":\"3f23a8c4-cd0e-a5a4-63f2-bb2841e95a01\",\"updateId\":\"1674170951\",\"createdAt\":\"1970-01-20T09:02:50Z\",\"commitHash\":\"1674170951\",\"platform\":\"android\"}],\"nextCursor\":null}", strings.TrimSpace(respRec.Body.String()))
}
