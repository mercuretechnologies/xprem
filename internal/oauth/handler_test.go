package oauth

import (
	"context"
	"encoding/json"
	"expo-open-ota/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeClientRepo struct {
	inserted []store.InsertOAuthClientParameters
}

func (f *fakeClientRepo) InsertOAuthClient(_ context.Context, params store.InsertOAuthClientParameters) error {
	f.inserted = append(f.inserted, params)
	return nil
}

func (f *fakeClientRepo) GetOAuthClient(_ context.Context, id string) (store.OAuthClient, error) {
	for _, params := range f.inserted {
		if params.ID == id {
			return store.OAuthClient{Id: params.ID, Name: params.Name, RedirectURIs: params.RedirectURIs}, nil
		}
	}
	return store.OAuthClient{}, &store.ErrResourceNotFound{Resource: "oauth client", Identifier: id}
}

type fakeCodeRepo struct {
	inserted []store.InsertOAuthAuthorizationCodeParameters
}

func (f *fakeCodeRepo) InsertOAuthAuthorizationCode(_ context.Context, params store.InsertOAuthAuthorizationCodeParameters) error {
	f.inserted = append(f.inserted, params)
	return nil
}

func (f *fakeCodeRepo) DeleteExpiredOAuthAuthorizationCodes(_ context.Context) error {
	return nil
}

func newTestHandler(t *testing.T) (*OAuthHandler, *fakeClientRepo) {
	t.Helper()
	handler, clientRepo, _ := newTestHandlerWithCodes(t)
	return handler, clientRepo
}

func newTestHandlerWithCodes(t *testing.T) (*OAuthHandler, *fakeClientRepo, *fakeCodeRepo) {
	t.Helper()
	t.Setenv("BASE_URL", "https://ota.example.com")
	clientRepo := &fakeClientRepo{}
	codeRepo := &fakeCodeRepo{}
	// A nil limiter allows everything; rate limiting has its own tests, and
	// token verification (the userRepo) has its own in internal/middleware.
	return NewOAuthHandler(NewOAuthService(clientRepo, codeRepo, nil), nil), clientRepo, codeRepo
}

func decodeJSON(t *testing.T, res *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	return body
}

func TestProtectedResourceMetadata(t *testing.T) {
	handler, _ := newTestHandler(t)
	res := httptest.NewRecorder()
	handler.ProtectedResourceMetadataHandler(res, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}
	body := decodeJSON(t, res)
	if body["resource"] != "https://ota.example.com/mcp" {
		t.Errorf("wrong resource: %v", body["resource"])
	}
	servers, _ := body["authorization_servers"].([]interface{})
	if len(servers) != 1 || servers[0] != "https://ota.example.com" {
		t.Errorf("wrong authorization_servers: %v", body["authorization_servers"])
	}
}

func TestAuthorizationServerMetadata(t *testing.T) {
	handler, _ := newTestHandler(t)
	res := httptest.NewRecorder()
	handler.AuthorizationServerMetadataHandler(res, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}
	body := decodeJSON(t, res)
	expectations := map[string]string{
		"issuer":                 "https://ota.example.com",
		"authorization_endpoint": "https://ota.example.com/oauth/authorize",
		"token_endpoint":         "https://ota.example.com/oauth/token",
		"registration_endpoint":  "https://ota.example.com/oauth/register",
	}
	for field, expected := range expectations {
		if body[field] != expected {
			t.Errorf("%s: expected %q, got %v", field, expected, body[field])
		}
	}
	methods, _ := body["code_challenge_methods_supported"].([]interface{})
	if len(methods) != 1 || methods[0] != "S256" {
		t.Errorf("wrong code_challenge_methods_supported: %v", body["code_challenge_methods_supported"])
	}
}

func TestWithCORSPreflight(t *testing.T) {
	called := false
	wrapped := WithCORS(func(w http.ResponseWriter, r *http.Request) { called = true })

	res := httptest.NewRecorder()
	wrapped(res, httptest.NewRequest(http.MethodOptions, "/oauth/register", nil))
	if called {
		t.Error("preflight must not reach the handler")
	}
	if res.Code != http.StatusNoContent {
		t.Errorf("expected 204 on preflight, got %d", res.Code)
	}
	if res.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing Access-Control-Allow-Origin")
	}
}

func registerBody(t *testing.T, payload map[string]interface{}) *strings.Reader {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReader(string(raw))
}

func TestRegisterClient(t *testing.T) {
	handler, repo := newTestHandler(t)
	res := httptest.NewRecorder()
	handler.RegisterHandler(res, httptest.NewRequest(http.MethodPost, "/oauth/register", registerBody(t, map[string]interface{}{
		"client_name":   "Claude Code",
		"redirect_uris": []string{"http://127.0.0.1:53422/callback", "https://claude.ai/api/callback"},
	})))

	if res.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", res.Code, res.Body.String())
	}
	body := decodeJSON(t, res)
	if body["client_id"] == "" || body["client_id"] == nil {
		t.Error("missing client_id")
	}
	if body["token_endpoint_auth_method"] != "none" {
		t.Errorf("wrong token_endpoint_auth_method: %v", body["token_endpoint_auth_method"])
	}
	if len(repo.inserted) != 1 {
		t.Fatalf("expected 1 stored client, got %d", len(repo.inserted))
	}
	if repo.inserted[0].ID != body["client_id"] {
		t.Error("stored client id differs from the returned client_id")
	}
}

func TestRegisterClientRejections(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]interface{}
	}{
		{"no redirect_uris", map[string]interface{}{"client_name": "x"}},
		{"http on a public host", map[string]interface{}{"redirect_uris": []string{"http://evil.example.com/callback"}}},
		{"relative uri", map[string]interface{}{"redirect_uris": []string{"/callback"}}},
		{"fragment in uri", map[string]interface{}{"redirect_uris": []string{"https://a.example.com/cb#frag"}}},
		{"confidential auth method", map[string]interface{}{
			"redirect_uris":              []string{"https://a.example.com/cb"},
			"token_endpoint_auth_method": "client_secret_basic",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, repo := newTestHandler(t)
			res := httptest.NewRecorder()
			handler.RegisterHandler(res, httptest.NewRequest(http.MethodPost, "/oauth/register", registerBody(t, tc.payload)))

			if res.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", res.Code, res.Body.String())
			}
			if body := decodeJSON(t, res); body["error"] != "invalid_client_metadata" {
				t.Errorf("expected RFC 7591 error shape, got: %s", res.Body.String())
			}
			if len(repo.inserted) != 0 {
				t.Error("a rejected registration must not store a client")
			}
		})
	}
}

func TestRegisterClientLoopbackHTTPAllowed(t *testing.T) {
	handler, _ := newTestHandler(t)
	for _, uri := range []string{"http://localhost:8080/cb", "http://127.0.0.1:1234/cb", "http://[::1]:9/cb"} {
		res := httptest.NewRecorder()
		handler.RegisterHandler(res, httptest.NewRequest(http.MethodPost, "/oauth/register", registerBody(t, map[string]interface{}{
			"redirect_uris": []string{uri},
		})))
		if res.Code != http.StatusCreated {
			t.Errorf("%s: expected 201, got %d: %s", uri, res.Code, res.Body.String())
		}
	}
}
