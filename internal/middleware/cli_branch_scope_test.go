package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"expo-open-ota/internal/database/postgres/pgdb"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/types"

	"github.com/gorilla/mux"
)

// recordingCliRepo accepts any credential and reports a key id, which is what
// makes the access policy run at all.
type recordingCliRepo struct{}

func (recordingCliRepo) ValidateCliCredential(context.Context, string, types.Auth) (int64, error) {
	return 42, nil
}

func (recordingCliRepo) GetApiKeyNameByID(context.Context, string, int64) (string, error) {
	return "ci", nil
}

// The rest of the repository interface, unused here: only the validation path
// runs in these tests.
func (recordingCliRepo) InsertApiKey(context.Context, string, string, string, string) (int64, error) {
	return 0, nil
}

func (recordingCliRepo) GetApiKeysMetadataByAppID(
	context.Context, string,
) ([]pgdb.GetApiKeysMetadataByAppIDRow, error) {
	return nil, nil
}

func (recordingCliRepo) RevokeApiKeyByID(context.Context, int64, string) (string, error) {
	return "", nil
}

// recordingPolicy captures the branch name the middleware handed the access
// policy, which is the whole subject of this test.
type recordingPolicy struct{ branchName string }

func (p *recordingPolicy) AuthorizeCliRequest(
	_ context.Context, _ string, _ int64, branchName string, _ netip.Addr,
) error {
	p.branchName = branchName
	return nil
}

// The two routes a publishing token may reach both carry a {BRANCH}, and the
// per-key protected-branch restriction is only looked up when a branch name is
// given (ee/apikeyrestrictions/service.go gates it on branchName != ""). The
// middleware used to hardcode an empty name, so a CI key marked as having no
// access to protected branches could still read production's runtime versions
// and update history: the restriction meant "cannot write" while reading, in
// the dashboard, as "has nothing to do with protected branches".
func TestCliAuthPassesTheRouteBranchToTheAccessPolicy(t *testing.T) {
	policy := &recordingPolicy{}
	cliAuth := services.NewCliAuthService(recordingCliRepo{}, policy)

	router := mux.NewRouter()
	api := router.PathPrefix("/api").Subrouter()
	api.Use(NewAuthMiddleware(services.NewDashboardAuthService(nil), cliAuth))
	api.HandleFunc("/apps/{APP_ID}/branch/{BRANCH}/runtimeVersions",
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }).
		Methods(http.MethodGet)

	req := httptest.NewRequest(http.MethodGet, "/api/apps/app-1/branch/production/runtimeVersions", nil)
	req.Header.Set("Use-Cli-Auth", "true")
	req.Header.Set("Authorization", "Bearer expo_test_token")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("the credential is valid, the route should answer: got %d", recorder.Code)
	}
	if policy.branchName != "production" {
		t.Fatalf("the policy must be told which branch is being read, got %q", policy.branchName)
	}
}

// A route without a {BRANCH} still passes an empty name, which is correct: the
// restriction has no branch to check, and the IP allowlist runs regardless.
func TestCliAuthPassesNoBranchWhenTheRouteHasNone(t *testing.T) {
	policy := &recordingPolicy{branchName: "sentinel"}
	cliAuth := services.NewCliAuthService(recordingCliRepo{}, policy)

	router := mux.NewRouter()
	api := router.PathPrefix("/api").Subrouter()
	api.Use(NewAuthMiddleware(services.NewDashboardAuthService(nil), cliAuth))
	api.HandleFunc("/apps/{APP_ID}/updates",
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }).
		Methods(http.MethodGet)

	req := httptest.NewRequest(http.MethodGet, "/api/apps/app-1/updates", nil)
	req.Header.Set("Use-Cli-Auth", "true")
	req.Header.Set("Authorization", "Bearer expo_test_token")
	router.ServeHTTP(httptest.NewRecorder(), req)

	if policy.branchName != "" {
		t.Fatalf("a route with no branch must not invent one, got %q", policy.branchName)
	}
}
