package infrastructure

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"xprem/internal/oauth"

	"github.com/gorilla/mux"
)

// The RFC 8414 / 9728 inserted discovery documents live at the host root, so
// they only exist on the outer router mountPublicPath builds.
func TestMountPublicPathServesInsertedOAuthDiscovery(t *testing.T) {
	t.Setenv("BASE_URL", "https://api.example.com/a/b")
	t.Setenv("SERVE_FROM_SUB_PATH", "true")
	container := &AppContainer{
		OAuthHandler: oauth.NewOAuthHandler(oauth.NewOAuthService(nil, nil, nil, nil), nil),
	}
	inner := mux.NewRouter()
	inner.SkipClean(true)
	registerOAuthRoutes(inner, container)
	router := mountPublicPath(inner, container)

	get := func(path string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		return res
	}

	res := get("/.well-known/oauth-authorization-server/a/b")
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200 on the inserted AS metadata, got %d", res.Code)
	}
	if res.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("discovery must stay CORS-readable for browser MCP clients")
	}
	var asMeta struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &asMeta); err != nil {
		t.Fatalf("invalid AS metadata JSON: %v", err)
	}
	if asMeta.Issuer != "https://api.example.com/a/b" {
		t.Errorf("issuer must be BASE_URL, got %q", asMeta.Issuer)
	}
	if asMeta.AuthorizationEndpoint != "https://api.example.com/a/b/oauth/authorize" {
		t.Errorf("authorization_endpoint must live under the prefix, got %q", asMeta.AuthorizationEndpoint)
	}

	res = get("/.well-known/oauth-protected-resource/a/b/mcp")
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200 on the inserted resource metadata, got %d", res.Code)
	}
	var prMeta struct {
		Resource string `json:"resource"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &prMeta); err != nil {
		t.Fatalf("invalid resource metadata JSON: %v", err)
	}
	if prMeta.Resource != "https://api.example.com/a/b/mcp" {
		t.Errorf("resource must be BASE_URL/mcp, got %q", prMeta.Resource)
	}

	// The suffixed form non-conforming clients probe, reached through the
	// prefix strip.
	if res := get("/a/b/.well-known/oauth-authorization-server"); res.Code != http.StatusOK {
		t.Fatalf("expected 200 on the suffixed form, got %d", res.Code)
	}

	// The inserted routes must not leak to other paths.
	if res := get("/.well-known/oauth-authorization-server/other"); res.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a foreign inserted path, got %d", res.Code)
	}
}
