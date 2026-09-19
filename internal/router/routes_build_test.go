package infrastructure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"xprem/config"
	"xprem/ee/apikeyrestrictions"
	"xprem/ee/licensing"
	"xprem/internal/android/androidtest"
	"xprem/internal/bucket"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/types"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

const buildID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

type buildIdentifierRepo struct {
	services.AppIdentifierRepository
	app       string
	platform  types.Platform
	allocated int64
	lookupErr error
	idLookups int
}

func (repo *buildIdentifierRepo) GetAppIdentifierByID(_ context.Context, app, id string) (*store.AppIdentifierRef, error) {
	repo.app = app
	repo.idLookups++
	if app != "app-1" || id != buildID {
		return nil, nil
	}
	return &store.AppIdentifierRef{Id: buildID, Platform: repo.platform}, nil
}

func (repo *buildIdentifierRepo) GetAppIdentifierByPlatformAndIdentifier(_ context.Context, app string, platform types.Platform, identifier string) (*store.AppIdentifierRef, error) {
	repo.app = app
	if repo.lookupErr != nil {
		return nil, repo.lookupErr
	}
	if app != "app-1" || platform != repo.platform || identifier != "com.example.app" {
		return nil, nil
	}
	return &store.AppIdentifierRef{Id: buildID, Platform: repo.platform, Identifier: identifier}, nil
}

type recordingBuildPolicy struct {
	requests     []apikeyrestrictions.BuildRequest
	environments []apikeyrestrictions.EnvironmentRequest
	err          error
}

func (p *recordingBuildPolicy) AuthorizeEnvironment(_ context.Context, req apikeyrestrictions.EnvironmentRequest) error {
	p.environments = append(p.environments, req)
	return p.err
}

func (p *recordingBuildPolicy) AuthorizeBuild(_ context.Context, req apikeyrestrictions.BuildRequest) error {
	p.requests = append(p.requests, req)
	return p.err
}
func TestBuildGuardAuthorizesResolvedTargetBeforeSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, app, id string
		platform      types.Platform
		deny          error
		status        int
		decisions     int
	}{
		{"allowed", "app-1", buildID, "android", nil, 200, 1},
		{"uppercase UUID", "app-1", "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA", "android", nil, 200, 1},
		{"denied", "app-1", buildID, "android", services.ErrCliAccessDenied, 403, 1},
		{"outage", "app-1", buildID, "android", services.ErrCliAuthUnavailable, 500, 1},
		{"revoked", "app-1", buildID, "android", apikeyrestrictions.ErrApiKeyNotFound, 401, 1},
		{"foreign app", "app-2", buildID, "android", nil, 404, 0},
		{"foreign identifier", "app-1", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "android", nil, 404, 0},
		{"ios", "app-1", buildID, "ios", nil, 200, 1},
		{"malformed", "app-1", "bad", "android", nil, 400, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &buildIdentifierRepo{platform: tc.platform}
			policy := &recordingBuildPolicy{err: tc.deny}
			group := buildGroup{cliAuth: services.NewCliAuthService(acceptingCliRepo{}), apiKeyAccess: policy, identifiers: repo}
			router := mux.NewRouter()
			called := false
			router.Handle("/{APP_ID}/build/{IDENTIFIER_ID}/environment", group.guard(apikeyrestrictions.BuildActionCreate)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				require.NotNil(t, services.CliAuthFromContext(r.Context()))
				require.Equal(t, buildID, services.BuildIdentifierFromContext(r.Context()))
				require.Equal(t, tc.id, mux.Vars(r)["IDENTIFIER_ID"])
				w.WriteHeader(200)
			})))
			req := httptest.NewRequest("GET", "/"+tc.app+"/build/"+tc.id+"/environment?channel=production", nil)
			req.Header.Set("Authorization", "Bearer eoo_key")
			req.RemoteAddr = "192.0.2.1:4000"
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Equal(t, tc.status == 200, called)
			require.Len(t, policy.requests, tc.decisions)
			require.Empty(t, w.Header().Get("Cache-Control"))
			if tc.decisions > 0 {
				decision := policy.requests[0]
				require.Equal(t, tc.app, decision.AppID)
				require.Equal(t, buildID, decision.AppIdentifierID)
				require.Equal(t, apikeyrestrictions.BuildActionCreate, decision.Action)
				require.Equal(t, int64(42), decision.APIKeyID)
				require.Equal(t, "192.0.2.1", decision.ClientIP.String())
			}
		})
	}
}

// Authentication failures must prevent even the target lookup.
func TestBuildGuardAuthenticationFailure(t *testing.T) {
	group := buildGroup{cliAuth: services.NewCliAuthService(failingBuildAuth{}), identifiers: nil}
	w := httptest.NewRecorder()
	group.guard(apikeyrestrictions.BuildActionCreate)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("secret handler reached") })).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	require.Equal(t, 401, w.Code)
}

type failingBuildAuth struct{ acceptingCliRepo }

func (failingBuildAuth) ValidateCliCredential(context.Context, string, types.Auth) (int64, error) {
	return 0, errors.New("invalid")
}

type buildAccessRepo struct {
	apikeyrestrictions.ApiKeyAccessRepository
	access apikeyrestrictions.ApiKeyAccess
	app    string
}

func (repo *buildAccessRepo) GetAccess(_ context.Context, app string, key int64) (apikeyrestrictions.ApiKeyAccess, error) {
	repo.app = app
	if app != "app-1" || key != 42 {
		return apikeyrestrictions.ApiKeyAccess{}, apikeyrestrictions.ErrApiKeyNotFound
	}
	return repo.access, nil
}

// Exercise both real route registrations with the real Enterprise policy, not
// only a stubbed allow/deny decision. Only Build rules restrict build exports.
func TestBuildRoutesEnterpriseDomains(t *testing.T) {
	t.Setenv("AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID", "")
	t.Setenv("DB_KEYS_MASTER_KEY_B64", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	previous := licensing.Current()
	licensing.Activate(licensing.License{PlanCode: licensing.PlanEnterprise})
	t.Cleanup(func() {
		if previous == nil {
			licensing.Deactivate()
		} else {
			licensing.Activate(*previous)
		}
	})
	for _, tc := range []struct {
		name   string
		access apikeyrestrictions.ApiKeyAccess
		status int
	}{
		{"build", apikeyrestrictions.ApiKeyAccess{BuildRules: []apikeyrestrictions.BuildRule{{AppIdentifierID: buildID, Actions: []apikeyrestrictions.BuildAction{apikeyrestrictions.BuildActionCreate}}}}, 200},
		{"ota", apikeyrestrictions.ApiKeyAccess{UpdateRules: []apikeyrestrictions.UpdateRule{{Pattern: "*", Actions: []apikeyrestrictions.UpdateAction{apikeyrestrictions.UpdateActionPublish}}}}, 200},
		{"submit", apikeyrestrictions.ApiKeyAccess{SubmitRules: []apikeyrestrictions.SubmitRule{{AppIdentifierID: buildID, Destination: apikeyrestrictions.SubmitDestinationInternal, Actions: []apikeyrestrictions.SubmitAction{apikeyrestrictions.SubmitActionUpload}}}}, 200},
		{"empty", apikeyrestrictions.ApiKeyAccess{}, 200},
		{"other identifier", apikeyrestrictions.ApiKeyAccess{BuildRules: []apikeyrestrictions.BuildRule{{AppIdentifierID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", Actions: []apikeyrestrictions.BuildAction{apikeyrestrictions.BuildActionCreate}}}}, 403},
		{"blocked IP", apikeyrestrictions.ApiKeyAccess{AllowedIps: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}, BuildRules: []apikeyrestrictions.BuildRule{{AppIdentifierID: buildID, Actions: []apikeyrestrictions.BuildAction{apikeyrestrictions.BuildActionCreate}}}}, 403},
	} {
		for _, target := range []struct {
			platform types.Platform
			endpoint string
		}{{"android", "environment"}, {"ios", "environment"}, {"android", "credentials/android"}, {"android", "build-number"}, {"ios", "build-number"}, {"android", "resolve/android/com.example.app"}, {"ios", "resolve/ios/com.example.app"}} {
			endpoint := target.endpoint
			t.Run(tc.name+"/"+string(target.platform)+"/"+endpoint, func(t *testing.T) {
				access := tc.access
				access.ApiKeyID = 42
				repo := &buildAccessRepo{access: access}
				identifiers := &buildIdentifierRepo{platform: target.platform}
				environments := services.NewEnvironmentService(nil)
				vault := &buildCredentialsRepo{}
				credentials := services.NewCredentialsService(vault, identifiers)
				if target.platform == types.PlatformAndroid {
					require.NoError(t, credentials.SaveAndroidCredentials(context.Background(), "app-1", buildID, services.AndroidCredentialsInput{
						KeystoreBase64:   base64.StdEncoding.EncodeToString(androidtest.JKSKeystore("store-pass", "key-pass", "upload")),
						KeystorePassword: "store-pass", KeyPassword: "key-pass", KeyAlias: "upload",
					}))
				}
				container := &AppContainer{AppRepo: buildAppRepo{}, CliAuthService: services.NewCliAuthService(acceptingCliRepo{}), ApiKeyAccessService: apikeyrestrictions.NewApiKeyAccessService(repo), AppIdentifierRepo: identifiers, BuildHandler: handlers.NewBuildHandler(environments, credentials, nil, services.NewAppIdentifierService(identifiers))}
				router := mux.NewRouter()
				registerBuildRoutes(router, container)
				method := http.MethodGet
				if endpoint == "build-number" {
					method = http.MethodPost
				}
				requestPath := "/app-1/build/" + buildID + "/" + endpoint
				if strings.HasPrefix(endpoint, "resolve/") {
					requestPath = "/app-1/build/" + endpoint
				}
				req := httptest.NewRequest(method, requestPath, nil)
				req.Header.Set("Authorization", "Bearer eoo_key")
				req.RemoteAddr = "192.0.2.1:4000"
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				require.Equal(t, tc.status, w.Code, w.Body.String())
				require.Equal(t, "app-1", repo.app)
				require.Equal(t, tc.status == 200 && endpoint == "credentials/android", vault.read)
				if tc.status == 200 && endpoint == "build-number" {
					require.Equal(t, int64(1), identifiers.allocated)
					require.JSONEq(t, `{"buildNumber":"1"}`, w.Body.String())
				} else {
					require.Zero(t, identifiers.allocated)
				}
				if tc.status == 200 && strings.HasPrefix(endpoint, "resolve/") {
					require.JSONEq(t, `{"identifierId":"`+buildID+`"}`, w.Body.String())
				}
				if tc.status == 200 && endpoint == "environment" {
					require.JSONEq(t, `{"environment":null,"variables":{}}`, w.Body.String())
				}
			})
		}
	}
}

type buildAppRepo struct{ services.AppRepository }

func (buildAppRepo) GetAppByID(context.Context, string) (config.AppConfig, error) {
	return config.AppConfig{}, nil
}

func TestBuildRoutePropagatesAction(t *testing.T) {
	// A sentinel action distinguishes propagation from a hardcoded create grant.
	// Route registration leaves validation to the policy, which must receive it unchanged.
	action := apikeyrestrictions.BuildAction("sentinel")
	policy := &recordingBuildPolicy{err: services.ErrCliAccessDenied}
	group := buildGroup{router: mux.NewRouter(), cliAuth: services.NewCliAuthService(acceptingCliRepo{}), apiKeyAccess: policy, identifiers: &buildIdentifierRepo{platform: "android"}}
	req := httptest.NewRequest(http.MethodGet, "/app-1/build/"+buildID, nil)
	w := httptest.NewRecorder()
	group.route(http.MethodGet, "/{APP_ID}/build/{IDENTIFIER_ID}", func(http.ResponseWriter, *http.Request) { t.Fatal("denied action reached handler") }, action)
	group.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Len(t, policy.requests, 1)
	require.Equal(t, action, policy.requests[0].Action)
}

// A successfully authenticated Expo credential has no registered API key.
// Build must refuse it before touching even the identifier repository.
func TestBuildGuardRejectsUnregisteredCredential(t *testing.T) {
	for _, keyID := range []int64{0, -1} {
		group := buildGroup{cliAuth: services.NewCliAuthService(buildAuthKeyID{keyID: keyID})}
		w := httptest.NewRecorder()
		group.guard(apikeyrestrictions.BuildActionCreate)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("unregistered credential reached build handler")
		})).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		require.Equal(t, http.StatusUnauthorized, w.Code)
	}
}

type buildAuthKeyID struct {
	acceptingCliRepo
	keyID int64
}

func (repo buildAuthKeyID) ValidateCliCredential(context.Context, string, types.Auth) (int64, error) {
	return repo.keyID, nil
}

func TestBuildGuardAllowsRegisteredCommunityToken(t *testing.T) {
	previous := licensing.Current()
	licensing.Deactivate()
	t.Cleanup(func() {
		if previous != nil {
			licensing.Activate(*previous)
		}
	})
	group := buildGroup{
		cliAuth:      services.NewCliAuthService(acceptingCliRepo{}),
		apiKeyAccess: apikeyrestrictions.NewApiKeyAccessService(&buildAccessRepo{}),
		identifiers:  &buildIdentifierRepo{platform: "android"},
	}
	req := mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/", nil), map[string]string{"APP_ID": "app-1", "IDENTIFIER_ID": buildID})
	w := httptest.NewRecorder()
	group.guard(apikeyrestrictions.BuildActionCreate)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, int64(42), services.CliAuthFromContext(r.Context()).KeyID)
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

// The actual credentials service validates Android before reading/decrypting
// the keystore. The common guard must still authorize the target first.
func TestAndroidBuildCredentialsPlatform(t *testing.T) {
	for _, platform := range []types.Platform{types.PlatformAndroid, types.PlatformIOS} {
		t.Run(string(platform), func(t *testing.T) {
			identifiers := &buildIdentifierRepo{platform: platform}
			credentials := &buildCredentialsRepo{}
			handler := handlers.NewBuildHandler(nil, services.NewCredentialsService(credentials, identifiers), nil, nil)
			policy := &recordingBuildPolicy{}
			group := buildGroup{router: mux.NewRouter(), cliAuth: services.NewCliAuthService(acceptingCliRepo{}), apiKeyAccess: policy, identifiers: identifiers}
			group.route(http.MethodGet, "/{APP_ID}/build/{IDENTIFIER_ID}/credentials/android", handler.AndroidCredentials, apikeyrestrictions.BuildActionCreate)
			req := httptest.NewRequest(http.MethodGet, "/app-1/build/"+buildID+"/credentials/android", nil)
			w := httptest.NewRecorder()
			group.router.ServeHTTP(w, req)
			require.Len(t, policy.requests, 1)
			require.Equal(t, buildID, policy.requests[0].AppIdentifierID)
			if platform == types.PlatformIOS {
				require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
				require.False(t, credentials.read)
			} else {
				// No stored key in this fixture; reaching the vault yields its normal 404.
				require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
				require.True(t, credentials.read)
			}
		})
	}
}

type buildCredentialsRepo struct {
	services.CredentialsRepository
	credentials *store.SealedAndroidCredentials
	read        bool
}

func (repo *buildCredentialsRepo) GetAndroidCredentials(context.Context, string) (*store.SealedAndroidCredentials, error) {
	repo.read = true
	return repo.credentials, nil
}

func (repo *buildCredentialsRepo) UpsertAndroidCredentials(_ context.Context, _ string, credentials store.SealedAndroidCredentials) error {
	repo.credentials = &credentials
	return nil
}

func (repo *buildIdentifierRepo) AllocateBuildNumber(ctx context.Context, app, id string, next func(types.Platform, string) (string, error)) (*store.AppIdentifierRef, error) {
	ref, err := repo.GetAppIdentifierByID(ctx, app, id)
	if err != nil || ref == nil {
		return nil, &store.ErrResourceNotFound{Resource: "app identifier", Identifier: id}
	}
	previous := strconv.FormatInt(repo.allocated, 10)
	value, err := next(ref.Platform, previous)
	if err != nil {
		return nil, err
	}
	repo.allocated++
	ref.PreviousBuildNumber = previous
	ref.BuildNumber = value
	return ref, nil
}

func TestBuildResolveTargetedLookup(t *testing.T) {
	for _, tc := range []struct {
		name, app, identifier       string
		platform, requestedPlatform types.Platform
		err                         error
		status                      int
	}{
		{"android", "app-1", "com.example.app", types.PlatformAndroid, types.PlatformAndroid, nil, 200},
		{"ios", "app-1", "com.example.app", types.PlatformIOS, types.PlatformIOS, nil, 200},
		{"unknown", "app-1", "com.example.missing", types.PlatformAndroid, types.PlatformAndroid, nil, 404},
		{"foreign app", "app-2", "com.example.app", types.PlatformAndroid, types.PlatformAndroid, nil, 404},
		{"android does not resolve ios", "app-1", "com.example.app", types.PlatformIOS, types.PlatformAndroid, nil, 404},
		{"ios does not resolve android", "app-1", "com.example.app", types.PlatformAndroid, types.PlatformIOS, nil, 404},
		{"unknown platform", "app-1", "com.example.app", types.PlatformAndroid, "windows", nil, 400},
		{"database error", "app-1", "com.example.app", types.PlatformAndroid, types.PlatformAndroid, errors.New("database unavailable"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &buildIdentifierRepo{platform: tc.platform, lookupErr: tc.err}
			policy := &recordingBuildPolicy{}
			group := buildGroup{cliAuth: services.NewCliAuthService(acceptingCliRepo{}), apiKeyAccess: policy, identifiers: repo}
			router := mux.NewRouter()
			router.Handle("/{APP_ID}/build/resolve/{PLATFORM}/{APPLICATION_ID}", group.guard(apikeyrestrictions.BuildActionCreate)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, buildID, services.BuildIdentifierFromContext(r.Context()))
				require.Empty(t, mux.Vars(r)["IDENTIFIER_ID"])
				require.Equal(t, tc.identifier, mux.Vars(r)["APPLICATION_ID"])
				w.WriteHeader(200)
			})))
			req := httptest.NewRequest("GET", "/"+tc.app+"/build/resolve/"+string(tc.requestedPlatform)+"/"+tc.identifier, nil)
			req.Header.Set("Authorization", "Bearer eoo_key")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
			if tc.status == http.StatusBadRequest {
				require.Contains(t, recorder.Body.String(), "platform")
				require.Empty(t, repo.app, "invalid platforms must fail before identifier lookup")
			}
			if tc.err != nil {
				var problem handlers.APIError
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem))
				require.Equal(t, "Could not retrieve build inputs.", problem.Detail)
			}
			require.Zero(t, repo.idLookups, "resolution must not repeat the lookup by UUID")
			if tc.status == 200 {
				require.Len(t, policy.requests, 1)
				require.Equal(t, buildID, policy.requests[0].AppIdentifierID)
				require.Equal(t, apikeyrestrictions.BuildActionCreate, policy.requests[0].Action)
			} else {
				require.Empty(t, policy.requests)
			}
		})
	}
}

// Every build registry mutation runs behind the same guard as the build
// inputs, including uploads that also require a per-build token.
func TestBuildRegistryRoutesRequireBuildCreate(t *testing.T) {
	previous := licensing.Current()
	licensing.Activate(licensing.License{PlanCode: licensing.PlanEnterprise})
	t.Cleanup(func() {
		if previous == nil {
			licensing.Deactivate()
		} else {
			licensing.Activate(*previous)
		}
	})
	registry := handlers.NewBuildRegistryHandler(services.NewBuildService(nil, nil, nil))
	endpoints := []struct{ method, suffix, body string }{
		{http.MethodPost, "/artifacts/" + buildID + "/logs", `{"offset":0,"content":"compile\n"}`},
		{http.MethodPut, "/artifacts/" + buildID + "/start", `{"artifactType":"apk","metadata":{"profile":"p","cliVersion":"1","startedAt":"2026-09-08T10:00:00Z"}}`},
		{http.MethodPut, "/artifacts/" + buildID, `{"artifactType":"apk","size":1,"sha256":"` + strings.Repeat("a", 64) + `","metadata":{"profile":"p","cliVersion":"1","buildNumber":"1","fingerprint":"` + strings.Repeat("a", 40) + `","startedAt":"2026-09-08T10:00:00Z","finishedAt":"2026-09-08T10:01:00Z"}}`},
		{http.MethodPost, "/artifacts/" + buildID + "/failed", `{"finishedAt":"2026-09-08T10:00:00Z"}`},
		{http.MethodPost, "/artifacts/" + buildID + "/complete", ""},
		{http.MethodPut, "/artifacts/" + buildID + "/upload", "artifact bytes"},
		{http.MethodPost, "/cache/uploads", `{"namespace":"gradle","key":"archive-v1-` + strings.Repeat("a", 64) + `","size":1,"sha256":"` + strings.Repeat("a", 64) + `"}`},
		{http.MethodPost, "/cache/uploads/" + buildID + "/complete", ""},
		{http.MethodPut, "/cache/uploads/" + buildID, "cache bytes"},
		{http.MethodGet, "/cache/uploads/" + buildID + "/download", ""},
		{http.MethodGet, "/cache/gradle/archive-v1-" + strings.Repeat("a", 64), ""},
	}
	granted := apikeyrestrictions.ApiKeyAccess{ApiKeyID: 42, BuildRules: []apikeyrestrictions.BuildRule{{AppIdentifierID: buildID, Actions: []apikeyrestrictions.BuildAction{apikeyrestrictions.BuildActionCreate}}}}
	elsewhere := apikeyrestrictions.ApiKeyAccess{ApiKeyID: 42, BuildRules: []apikeyrestrictions.BuildRule{{AppIdentifierID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", Actions: []apikeyrestrictions.BuildAction{apikeyrestrictions.BuildActionCreate}}}}
	blockedIP := granted
	blockedIP.AllowedIps = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}
	for _, tc := range []struct {
		name   string
		auth   services.CliAuthRepository
		access apikeyrestrictions.ApiKeyAccess
		id     string
		status int
	}{
		{"allowed", acceptingCliRepo{}, granted, buildID, http.StatusBadRequest},
		{"denied", acceptingCliRepo{}, elsewhere, buildID, http.StatusForbidden},
		{"blocked IP", acceptingCliRepo{}, blockedIP, buildID, http.StatusForbidden},
		{"unauthenticated", failingBuildAuth{}, granted, buildID, http.StatusUnauthorized},
		{"unregistered token", buildAuthKeyID{keyID: 0}, granted, buildID, http.StatusUnauthorized},
		{"foreign identifier", acceptingCliRepo{}, granted, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", http.StatusNotFound},
	} {
		for _, endpoint := range endpoints {
			t.Run(tc.name+" "+endpoint.method+" "+endpoint.suffix, func(t *testing.T) {
				access := &buildAccessRepo{access: tc.access}
				container := &AppContainer{AppRepo: buildAppRepo{}, CliAuthService: services.NewCliAuthService(tc.auth), ApiKeyAccessService: apikeyrestrictions.NewApiKeyAccessService(access), AppIdentifierRepo: &buildIdentifierRepo{platform: types.PlatformAndroid}, BuildHandler: handlers.NewBuildHandler(nil, nil, nil, nil), BuildRegistryHandler: registry, BuildCacheHandler: handlers.NewBuildCacheHandler(services.NewBuildCacheService(nil, nil))}
				router := mux.NewRouter()
				registerBuildRoutes(router, container)
				body := strings.NewReader(endpoint.body)
				req := httptest.NewRequest(endpoint.method, "/app-1/build/"+tc.id+endpoint.suffix, body)
				req.Header.Set("Authorization", "Bearer eoo_key")
				req.RemoteAddr = "192.0.2.1:4000"
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				require.Equal(t, tc.status, w.Code, w.Body.String())
				require.Equal(t, tc.status == http.StatusBadRequest || tc.status == http.StatusForbidden, access.app != "", "the policy is consulted only for an authenticated, resolvable target")
				if tc.status == http.StatusBadRequest {
					require.Contains(t, w.Body.String(), "stateless mode", "the guard let the request through to the registry handler")
				} else {
					require.NotContains(t, w.Body.String(), "stateless mode", "the registry handler must not run")
					require.Equal(t, len(endpoint.body), body.Len(), "access must be denied before reading the upload")
				}
			})
		}
	}
}

func TestBuildRegistryRouteMethodsAndPublicPaths(t *testing.T) {
	registry := handlers.NewBuildRegistryHandler(services.NewBuildService(nil, nil, nil))
	access := &buildAccessRepo{}
	container := &AppContainer{AppRepo: buildAppRepo{}, CliAuthService: services.NewCliAuthService(acceptingCliRepo{}), ApiKeyAccessService: apikeyrestrictions.NewApiKeyAccessService(access), AppIdentifierRepo: &buildIdentifierRepo{platform: types.PlatformAndroid}, BuildHandler: handlers.NewBuildHandler(nil, nil, nil, nil), BuildRegistryHandler: registry}
	router := mux.NewRouter()
	registerBuildRoutes(router, container)
	registerLinkRoutes(router, container)
	for _, tc := range []struct {
		method, target string
		status         int
	}{
		{http.MethodGet, "/app-1/build/" + buildID + "/artifacts/" + buildID + "/start", http.StatusNotFound},
		{http.MethodPost, "/app-1/build/" + buildID + "/artifacts/" + buildID + "/start", http.StatusNotFound},
		{http.MethodPut, "/app-1/build/" + buildID + "/artifacts/" + buildID + "/failed", http.StatusNotFound},
		{http.MethodGet, "/app-1/build/" + buildID + "/artifacts/" + buildID, http.StatusNotFound},
		{http.MethodPut, "/build-uploads/forged-grant", http.StatusNotFound},
		{http.MethodPost, "/build-uploads/forged-grant", http.StatusNotFound},
		{http.MethodGet, "/build-shares/" + strings.Repeat("0", 64), http.StatusBadRequest},
		{http.MethodGet, "/build-shares/" + strings.Repeat("0", 64) + "/download", http.StatusNotFound},
		{http.MethodGet, "/build-shares/short", http.StatusBadRequest},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader("{}"))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
		})
	}
	require.Empty(t, access.app, "removed paths and wrong methods never consult the build policy")
}

type localUploadCliRepo struct{ acceptingCliRepo }

func (localUploadCliRepo) ValidateCliCredential(_ context.Context, app string, auth types.Auth) (int64, error) {
	if app != "app-1" || auth.Token == nil || *auth.Token != "eoo_valid" {
		return 0, services.ErrUnauthorized
	}
	return 42, nil
}

type localUploadBuildRepo struct{ services.BuildRepository }

func (localUploadBuildRepo) Get(_ context.Context, app, id string) (*types.BuildRecord, error) {
	if app != "app-1" || id != buildID {
		return nil, &store.ErrResourceNotFound{Resource: "build", Identifier: id}
	}
	return &types.BuildRecord{ID: id, AppID: app, AppIdentifierID: buildID, ArtifactType: types.BuildArtifactAPK, Status: types.BuildStatusUploading, Size: 3}, nil
}

func TestBuildLocalUploadRequiresBothTokensAndBuildPermission(t *testing.T) {
	t.Setenv("JWT_SECRET", "upload-test-secret")
	previous := licensing.Current()
	licensing.Activate(licensing.License{PlanCode: licensing.PlanEnterprise})
	t.Cleanup(func() {
		if previous == nil {
			licensing.Deactivate()
		} else {
			licensing.Activate(*previous)
		}
	})
	grant, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "build-upload", "exp": time.Now().Add(time.Minute).Unix(),
		"appId": "app-1", "identifierId": buildID, "buildId": buildID,
	}).SignedString([]byte("upload-test-secret"))
	require.NoError(t, err)
	for _, tc := range []struct {
		name, bearer, token, query string
		denied                     bool
		status                     int
	}{
		{"both tokens", "eoo_valid", grant, "", false, http.StatusNoContent},
		{"missing EOO", "", grant, "", false, http.StatusUnauthorized},
		{"invalid EOO", "eoo_invalid", grant, "", false, http.StatusUnauthorized},
		{"upload grant is not EOO", grant, grant, "", false, http.StatusUnauthorized},
		{"missing grant", "eoo_valid", "", "", false, http.StatusUnauthorized},
		{"grant in query", "eoo_valid", "", "?token=" + grant, false, http.StatusUnauthorized},
		{"invalid grant", "eoo_valid", "forged", "", false, http.StatusUnauthorized},
		{"denied identifier", "eoo_valid", grant, "", true, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			identifier := buildID
			if tc.denied {
				identifier = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
			}
			access := &buildAccessRepo{access: apikeyrestrictions.ApiKeyAccess{ApiKeyID: 42, BuildRules: []apikeyrestrictions.BuildRule{{AppIdentifierID: identifier, Actions: []apikeyrestrictions.BuildAction{apikeyrestrictions.BuildActionCreate}}}}}
			identifiers := &buildIdentifierRepo{platform: types.PlatformAndroid}
			service := services.NewBuildService(localUploadBuildRepo{}, identifiers, &bucket.LocalBucket{BasePath: root})
			container := &AppContainer{AppRepo: buildAppRepo{}, CliAuthService: services.NewCliAuthService(localUploadCliRepo{}), ApiKeyAccessService: apikeyrestrictions.NewApiKeyAccessService(access), AppIdentifierRepo: identifiers, BuildHandler: handlers.NewBuildHandler(nil, nil, nil, nil), BuildRegistryHandler: handlers.NewBuildRegistryHandler(service)}
			router := mux.NewRouter()
			registerBuildRoutes(router, container)
			body := strings.NewReader("apk")
			req := httptest.NewRequest(http.MethodPut, "/app-1/build/"+buildID+"/artifacts/"+buildID+"/upload"+tc.query, body)
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			req.Header.Set(bucket.LocalUploadTokenHeader, tc.token)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == http.StatusNoContent {
				key := (bucket.BuildArtifact{IdentifierID: buildID, BuildID: buildID, Type: types.BuildArtifactAPK}).Key(true)
				contents, err := os.ReadFile(filepath.Join(root, key))
				require.NoError(t, err)
				require.Equal(t, "apk", string(contents))
			} else {
				require.Equal(t, 3, body.Len(), "authorization failures must precede reading the body")
				entries, err := os.ReadDir(root)
				require.NoError(t, err)
				require.Empty(t, entries)
			}
		})
	}
}

func TestEnvironmentAuthorizerJudgesWithTheAuthenticatedKey(t *testing.T) {
	policy := &recordingBuildPolicy{}
	authorize := environmentAuthorizer(policy)
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "203.0.113.7:4242"

	require.ErrorIs(t, authorize(request, "production"), services.ErrUnauthorized, "a request the guard did not authenticate is refused")
	require.Empty(t, policy.environments)

	authenticated := request.WithContext(services.WithCliAuth(request.Context(), services.CliCredential{AppID: "app-1", KeyID: 42}))
	require.NoError(t, authorize(authenticated, "production"))
	require.Len(t, policy.environments, 1)
	require.Equal(t, "production", policy.environments[0].Environment)
	require.Equal(t, "app-1", policy.environments[0].AppID)
	require.EqualValues(t, 42, policy.environments[0].APIKeyID)
	require.Equal(t, "203.0.113.7", policy.environments[0].ClientIP.String())

	policy.err = services.ErrCliAccessDenied
	require.ErrorIs(t, authorize(authenticated, "production"), services.ErrCliAccessDenied)
}
