package infrastructure

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"expo-open-ota/ee/apikeyrestrictions"
	"expo-open-ota/internal/services"

	"github.com/gorilla/mux"
)

// recordingPolicy captures what a route handed the access decision.
type recordingPolicy struct {
	requests []apikeyrestrictions.CliRequest
	deny     error
}

func (p *recordingPolicy) Authorize(_ context.Context, req apikeyrestrictions.CliRequest) error {
	p.requests = append(p.requests, req)
	return p.deny
}

// serveTokenRequest registers one route through appGroup and sends a CLI
// credential at it.
func serveTokenRequest(t *testing.T, path, requestPath string, access AppAccess, policy cliAccessPolicy) (*httptest.ResponseRecorder, *services.CliCredential) {
	t.Helper()
	router := mux.NewRouter()
	group := appGroup{router: router.PathPrefix("/apps/{APP_ID}").Subrouter(), apiKeyAccess: policy}

	var seen *services.CliCredential
	group.route(http.MethodGet, path, func(w http.ResponseWriter, r *http.Request) {
		seen = services.CliAuthFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}, access)

	r := httptest.NewRequest(http.MethodGet, requestPath, nil)
	r = r.WithContext(services.WithCliAuth(r.Context(), services.CliCredential{AppID: "app-1", KeyID: 42}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w, seen
}

// TestTokenRouteMustNameABranch checks that registering a token route with no
// {BRANCH} in its path panics at boot.
func TestTokenRouteMustNameABranch(t *testing.T) {
	for name, path := range map[string]string{
		"no branch at all":         "/apiKeys",
		"branch spelled BRANCH_ID": "/branch/{BRANCH_ID}/updateChannelBranchMapping",
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected the registration to panic")
				}
			}()
			group := appGroup{router: mux.NewRouter(), apiKeyAccess: &recordingPolicy{}}
			group.route(http.MethodGet, path, func(http.ResponseWriter, *http.Request) {},
				AnyViewerOrToken(apikeyrestrictions.ActionRead))
		})
	}
}

// TestRouteWithoutTokenRefusesCliCredential checks that a route with no token
// declaration refuses a CLI credential, whatever its path.
func TestRouteWithoutTokenRefusesCliCredential(t *testing.T) {
	policy := &recordingPolicy{}
	w, _ := serveTokenRequest(t, "/branch/{BRANCH}/runtimeVersions",
		"/apps/app-1/branch/production/runtimeVersions", AnyViewer(), policy)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	if len(policy.requests) != 0 {
		t.Fatal("a route closed to tokens must not even ask the access policy")
	}
}

func TestTokenRouteAsksThePolicyWithItsBranchAndAction(t *testing.T) {
	policy := &recordingPolicy{}
	w, credential := serveTokenRequest(t, "/branch/{BRANCH}/runtimeVersions",
		"/apps/app-1/branch/production/runtimeVersions",
		AnyViewerOrToken(apikeyrestrictions.ActionRead), policy)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(policy.requests) != 1 {
		t.Fatalf("expected one access decision, got %d", len(policy.requests))
	}
	request := policy.requests[0]
	if request.Branch != "production" {
		t.Fatalf("expected the route's branch, got %q", request.Branch)
	}
	if request.Action != apikeyrestrictions.ActionRead {
		t.Fatalf("expected the route's action, got %q", request.Action)
	}
	if request.APIKeyID != 42 || request.AppID != "app-1" {
		t.Fatalf("expected the credential's key and app, got %+v", request)
	}
	if credential == nil || credential.KeyID != 42 {
		t.Fatalf("expected the credential on the handler's context, got %+v", credential)
	}
}

func TestTokenRouteRefusesWhatThePolicyDenies(t *testing.T) {
	policy := &recordingPolicy{deny: services.ErrCliAccessDenied}
	w, _ := serveTokenRequest(t, "/branch/{BRANCH}/runtimeVersions",
		"/apps/app-1/branch/production/runtimeVersions",
		AnyViewerOrToken(apikeyrestrictions.ActionRead), policy)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

// TestStatelessCredentialSkipsThePolicy checks that a stateless credential
// (no API key) never reaches the access policy.
func TestStatelessCredentialSkipsThePolicy(t *testing.T) {
	policy := &recordingPolicy{deny: errors.New("must not be called")}
	router := mux.NewRouter()
	group := appGroup{router: router.PathPrefix("/apps/{APP_ID}").Subrouter(), apiKeyAccess: policy}
	group.route(http.MethodGet, "/branch/{BRANCH}/runtimeVersions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, AnyViewerOrToken(apikeyrestrictions.ActionRead))

	r := httptest.NewRequest(http.MethodGet, "/apps/app-1/branch/production/runtimeVersions", nil)
	r = r.WithContext(services.WithCliAuth(r.Context(), services.CliCredential{AppID: "app-1"}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(policy.requests) != 0 {
		t.Fatal("a stateless credential must not reach the access policy")
	}
}

// TestUndeclaredAccessIsRefusedAtBoot checks that a route registered without
// an AppAccess declaration panics.
func TestUndeclaredAccessIsRefusedAtBoot(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected the registration to panic")
		}
	}()
	group := appGroup{router: mux.NewRouter(), apiKeyAccess: &recordingPolicy{}}
	group.route(http.MethodGet, "/branches", func(http.ResponseWriter, *http.Request) {}, AppAccess{})
}
