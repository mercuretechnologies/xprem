package infrastructure

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"expo-open-ota/ee/apikeyrestrictions"
	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/database/postgres/pgdb"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/types"

	"github.com/gorilla/mux"
)

// servePublishRequest builds the publish group the way registerPublishRoutes
// does, with a spy policy, and sends one request at it. The credential is
// resolved by the group itself, so cliAuth must accept whatever is presented.
func servePublishRequest(t *testing.T, method, path, requestPath string, action apikeyrestrictions.Action, policy *recordingPolicy) *httptest.ResponseRecorder {
	t.Helper()
	router := mux.NewRouter()
	group := publishGroup{
		router:       router.PathPrefix("/{APP_ID}").Subrouter(),
		cliAuth:      services.NewCliAuthService(acceptingCliRepo{}),
		apiKeyAccess: policy,
	}
	group.route(method, path, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, action)

	r := httptest.NewRequest(method, requestPath, nil)
	r.Header.Set("Authorization", "Bearer eoo_key")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

// Every publish route declares what it does to the branch it names, and that
// declaration is what the access rules are matched against. A route declared
// with the wrong action is a silent privilege change: a key granted only read
// on production could roll production back. This pins each one.
func TestPublishRoutesDeclareTheirAction(t *testing.T) {
	for _, tc := range []struct {
		path        string
		requestPath string
		action      apikeyrestrictions.Action
	}{
		{"/requestUploadUrl/{BRANCH}", "/app-1/requestUploadUrl/production", apikeyrestrictions.ActionPublish},
		{"/markUpdateAsUploaded/{BRANCH}", "/app-1/markUpdateAsUploaded/production", apikeyrestrictions.ActionPublish},
		{"/rollback/{BRANCH}", "/app-1/rollback/production", apikeyrestrictions.ActionRollback},
		{"/republish/{BRANCH}", "/app-1/republish/production", apikeyrestrictions.ActionRollback},
	} {
		t.Run(tc.path, func(t *testing.T) {
			policy := &recordingPolicy{}
			w := servePublishRequest(t, http.MethodPost, tc.path, tc.requestPath, tc.action, policy)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
			if len(policy.requests) != 1 {
				t.Fatalf("expected one access decision, got %d", len(policy.requests))
			}
			if got := policy.requests[0].Action; got != tc.action {
				t.Fatalf("expected action %q, got %q", tc.action, got)
			}
			if got := policy.requests[0].Branch; got != "production" {
				t.Fatalf("expected the route's branch, got %q", got)
			}
		})
	}
}

// The declarations the router actually registers, read from the same table it
// registers from. Re-declaring /rollback/{BRANCH} as a read, which is a silent
// privilege change, fails here.
func TestRegisteredPublishRoutesDeclareTheRightAction(t *testing.T) {
	expected := map[string]apikeyrestrictions.Action{
		"/requestUploadUrl/{BRANCH}":     apikeyrestrictions.ActionPublish,
		"/markUpdateAsUploaded/{BRANCH}": apikeyrestrictions.ActionPublish,
		"/rollback/{BRANCH}":             apikeyrestrictions.ActionRollback,
		"/republish/{BRANCH}":            apikeyrestrictions.ActionRollback,
	}
	if len(publishRouteTable) != len(expected) {
		t.Fatalf("the publish table holds %d routes, this test knows %d; a new route needs a declaration here too",
			len(publishRouteTable), len(expected))
	}
	for _, declaration := range publishRouteTable {
		want, known := expected[declaration.path]
		if !known {
			t.Fatalf("undeclared publish route %q", declaration.path)
		}
		if declaration.action != want {
			t.Fatalf("%s: expected action %q, got %q", declaration.path, want, declaration.action)
		}
	}
	// uploadLocalFile is registered on its own because its branch comes from a
	// token rather than the path; uploadTokenRoute hardcodes ActionPublish and
	// TestUploadTokenBranchResolution checks it.
}

// A resolver that names no branch has authorized nothing, and the guard says so
// itself rather than leaving the access rules to work it out. This is what a
// path variable renamed out from under the lookup would look like, and it must
// refuse rather than judge the request on an empty branch.
func TestGuardRefusesARequestWithNoResolvedBranch(t *testing.T) {
	policy := &recordingPolicy{}
	router := mux.NewRouter()
	group := publishGroup{
		router:       router.PathPrefix("/{APP_ID}").Subrouter(),
		cliAuth:      services.NewCliAuthService(acceptingCliRepo{}),
		apiKeyAccess: policy,
	}
	// Registered through the guard directly, since both route() and
	// uploadTokenRoute() refuse to produce this shape.
	emptyResolver := func(*http.Request) string { return "" }
	group.router.Handle("/publishSomething",
		group.guard(apikeyrestrictions.ActionPublish, emptyResolver)(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("the handler must not run without an authorized branch")
			}))).Methods(http.MethodPost)

	r := httptest.NewRequest(http.MethodPost, "/app-1/publishSomething", nil)
	r.Header.Set("Authorization", "Bearer eoo_key")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if len(policy.requests) != 0 {
		t.Fatal("a request with no branch must be refused without asking the access policy")
	}
}

// A publish route must name a branch, and must name a known action. Both are
// refused at boot rather than at the first request.
func TestPublishRouteDeclarationIsCheckedAtBoot(t *testing.T) {
	for name, register := range map[string]func(g publishGroup){
		"no branch in the path": func(g publishGroup) {
			g.route(http.MethodPost, "/uploadSomething", nil, apikeyrestrictions.ActionPublish)
		},
		"unknown action": func(g publishGroup) {
			g.route(http.MethodPost, "/rollback/{BRANCH}", nil, apikeyrestrictions.Action("delete"))
		},
		// The other door skips the branch requirement, so it has to refuse a
		// path that carries one rather than judge it on an absent token claim.
		"branch in the path of an upload-token route": func(g publishGroup) {
			g.uploadTokenRoute(http.MethodPut, "/uploadLocalFile/{BRANCH}", nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected the registration to panic")
				}
			}()
			register(publishGroup{router: mux.NewRouter(), apiKeyAccess: &recordingPolicy{}})
		})
	}
}

// uploadLocalFile names no branch: the guard reads it from the signed token,
// through the same validation the handler runs. A token that does not survive
// that validation names nothing, and a request that names no branch is refused
// before the access rules are consulted at all.
func TestUploadTokenBranchResolution(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("BASE_URL", "http://localhost:3000")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", t.TempDir())

	valid := mintUploadToken(t, "app-1", "production")
	for name, tc := range map[string]struct {
		query string
		// branch is what the policy must be handed; empty means the request
		// must be refused without the policy being asked.
		branch string
		status int
	}{
		"valid token":   {"?token=" + valid, "production", http.StatusOK},
		"no token":      {"", "", http.StatusForbidden},
		"garbage token": {"?token=not-a-jwt", "", http.StatusForbidden},
		// Signed with another secret: the claim is unreadable, so it names
		// nothing, exactly like a token that carries no claim at all.
		"foreign token": {"?token=" + mintForeignUploadToken(t, "app-1", "production"), "", http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			policy := &recordingPolicy{}
			router := mux.NewRouter()
			group := publishGroup{
				router:       router.PathPrefix("/{APP_ID}").Subrouter(),
				cliAuth:      services.NewCliAuthService(acceptingCliRepo{}),
				apiKeyAccess: policy,
			}
			group.uploadTokenRoute(http.MethodPut, "/uploadLocalFile", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			r := httptest.NewRequest(http.MethodPut, "/app-1/uploadLocalFile"+tc.query, nil)
			r.Header.Set("Authorization", "Bearer eoo_key")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)

			if w.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, w.Code, w.Body.String())
			}
			if tc.branch == "" {
				if len(policy.requests) != 0 {
					t.Fatalf("a token naming no branch must be refused without asking the policy, got %d calls",
						len(policy.requests))
				}
				return
			}
			if len(policy.requests) != 1 {
				t.Fatalf("expected one access decision, got %d", len(policy.requests))
			}
			if got := policy.requests[0].Branch; got != tc.branch {
				t.Fatalf("expected branch %q, got %q", tc.branch, got)
			}
			if got := policy.requests[0].Action; got != apikeyrestrictions.ActionPublish {
				t.Fatalf("expected the upload to be judged as a publish, got %q", got)
			}
		})
	}
}

// mintForeignUploadToken produces a well-formed upload token signed with a
// different secret, which is the shape an attacker can actually build.
func mintForeignUploadToken(t *testing.T, appId, branch string) string {
	t.Helper()
	original := os.Getenv("JWT_SECRET")
	t.Setenv("JWT_SECRET", "not-the-servers-secret")
	token := mintUploadToken(t, appId, branch)
	t.Setenv("JWT_SECRET", original)
	return token
}

// acceptingCliRepo authenticates any credential and reports a key id, which is
// what makes the access decision run at all.
type acceptingCliRepo struct{}

func (acceptingCliRepo) ValidateCliCredential(context.Context, string, types.Auth) (int64, error) {
	return 42, nil
}
func (acceptingCliRepo) GetApiKeyNameByID(context.Context, string, int64) (string, error) {
	return "ci", nil
}
func (acceptingCliRepo) InsertApiKey(context.Context, string, string, string, string) (int64, error) {
	return 0, nil
}
func (acceptingCliRepo) GetApiKeysMetadataByAppID(context.Context, string) ([]pgdb.GetApiKeysMetadataByAppIDRow, error) {
	return nil, nil
}
func (acceptingCliRepo) RevokeApiKeyByID(context.Context, int64, string) (string, error) {
	return "", nil
}

// mintUploadToken produces a real token the way the local bucket does, so the
// test exercises the signature and the claims the router actually reads.
func mintUploadToken(t *testing.T, appId, branch string) string {
	t.Helper()
	local := &bucket.LocalBucket{BasePath: t.TempDir()}
	uploadURL, err := local.RequestUploadUrlForFileUpdate(appId, branch, "1.0.0", "1", "bundle.js")
	if err != nil {
		t.Fatalf("could not mint an upload token: %v", err)
	}
	parsed, err := url.Parse(uploadURL)
	if err != nil {
		t.Fatalf("could not parse the upload url: %v", err)
	}
	return parsed.Query().Get("token")
}
