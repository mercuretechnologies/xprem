package test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	infrastructure "xprem/internal/router"

	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/assert"
)

// cliRequest sends an app-scoped request carrying a validated publishing
// credential. The Expo mock makes ValidateCliCredential succeed, so whatever
// status comes back is the ROUTE's answer and not an authentication failure.
func cliRequest(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	router := infrastructure.NewRouter(testContainer())
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(method, path, nil)
	req.Header.Set("Use-Cli-Auth", "true")
	req.Header.Set("Authorization", "Bearer expo_test_token")
	router.ServeHTTP(recorder, req)
	return recorder
}

func mockExpoForCli() {
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("operationName") == "FetchExpoUserAccountInformations" {
				return MockExpoAccountResponse(map[string]interface{}{
					"id": "123", "username": "test_username", "email": "test@example.com",
				})
			}
			return MockExpoBranchesMappingResponse(
				[]map[string]interface{}{{"id": "branch-1", "name": "branch-1"}},
				[]map[string]interface{}{{"id": "staging", "name": "staging", "branchMapping": "{\"data\":[{\"branchId\":\"branch-1\",\"branchMappingLogic\":\"true\"}],\"version\":0}"}},
			)
		})
}

// A publishing credential is usually a shared CI secret, and the app-scoped
// reads carry device-level data: a log record holds the client id, the session
// id and a body the application wrote, the device registry holds whatever
// metadata the app attached plus the city and the coordinates. None of that is
// what a token is for, and none of these routes is called by eoas.
//
// Every one of them used to answer a token, purely because it carried no
// permission gate: nothing had ever decided they should. This pins the
// decision, so a route added without an Access declaration naming a token
// cannot quietly join the list.
func TestPublishingTokenIsRefusedOnAppScopedReads(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForCli()

	for _, path := range []string{
		"/api/apps/test-app-id",
		"/api/apps/test-app-id/certificate",
		"/api/apps/test-app-id/branches",
		"/api/apps/test-app-id/channels",
		"/api/apps/test-app-id/updates",
		"/api/apps/test-app-id/apiKeys",
		"/api/apps/test-app-id/apiKeys/access",
		"/api/apps/test-app-id/identity/schema",
		"/api/apps/test-app-id/identity/devices",
		"/api/apps/test-app-id/identity/values",
		"/api/apps/test-app-id/identity/online",
		"/api/apps/test-app-id/identity/update-health",
		"/api/apps/test-app-id/observe/overview",
		"/api/apps/test-app-id/observe/check-ins",
		"/api/apps/test-app-id/observe/events",
		"/api/apps/test-app-id/observe/logs",
		"/api/apps/test-app-id/observe/breakdown",
		"/api/apps/test-app-id/observe/conditions",
		"/api/apps/test-app-id/observe/update-health/history",
		"/api/apps/test-app-id/observe/update-health/segments",
	} {
		assert.Equal(t, http.StatusForbidden, cliRequest(t, http.MethodGet, path).Code,
			"%s must not answer a publishing credential", path)
	}
}

// The two exceptions, and the reason the credential is accepted at all here:
// eoas asks which runtime versions a branch already has, then which updates
// that (branch, runtime version) pair holds, before it uploads anything.
func TestPublishingTokenStillReadsWhatEoasNeeds(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForCli()

	for _, path := range []string{
		"/api/apps/test-app-id/branch/branch-1/runtimeVersions",
		"/api/apps/test-app-id/branch/branch-1/runtimeVersion/1.0.0/updates",
	} {
		assert.Equal(t, http.StatusOK, cliRequest(t, http.MethodGet, path).Code,
			"%s is what the CLI reads before publishing", path)
	}
}

// Mutations were never reachable with a token: RequirePermission resolves a
// dashboard principal and a credential carries none. Pinned so the Access
// rewrite cannot have loosened them by accident.
func TestPublishingTokenIsRefusedOnAppScopedMutations(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoForCli()

	for _, tc := range []struct{ method, path string }{
		{http.MethodDelete, "/api/apps/test-app-id"},
		{http.MethodPatch, "/api/apps/test-app-id"},
		{http.MethodPost, "/api/apps/test-app-id/branches"},
		{http.MethodDelete, "/api/apps/test-app-id/branches/branch-1"},
		{http.MethodPost, "/api/apps/test-app-id/channels"},
		{http.MethodPost, "/api/apps/test-app-id/apiKeys"},
		{http.MethodPut, "/api/apps/test-app-id/identity/schema/plan"},
	} {
		assert.Equal(t, http.StatusForbidden, cliRequest(t, tc.method, tc.path).Code,
			"%s %s must not answer a publishing credential", tc.method, tc.path)
	}
}
