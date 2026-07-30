package infrastructure

import (
	"expo-open-ota/internal/mcp"
	"expo-open-ota/internal/oauth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func TestMCPEndpointCORS(t *testing.T) {
	t.Setenv("BASE_URL", "https://ota.example.com")
	t.Setenv("JWT_SECRET", "test-secret")
	container := &AppContainer{
		MCPHandler:   mcp.NewMCPHandler(mcp.NewMCPService()),
		OAuthService: oauth.NewOAuthService(nil, nil, nil, nil),
	}
	router := mux.NewRouter()
	registerMCPRoutes(router, container)

	// The preflight carries no Authorization header and must be answered
	// before authentication.
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodOptions, "/mcp", nil))
	if res.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on preflight, got %d", res.Code)
	}
	if res.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing Access-Control-Allow-Origin on preflight")
	}
	if allowed := res.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(allowed, "Mcp-Session-Id") {
		t.Errorf("Mcp-Session-Id must be an allowed request header, got %q", allowed)
	}

	// The unauthenticated 401 must stay CORS-readable, or a browser client
	// never sees the WWW-Authenticate challenge that starts the flow.
	res = httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a token, got %d", res.Code)
	}
	if res.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("the 401 must carry Access-Control-Allow-Origin")
	}
	if exposed := res.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(exposed, "WWW-Authenticate") {
		t.Errorf("WWW-Authenticate must be exposed to browser clients, got %q", exposed)
	}
	if res.Header().Get("WWW-Authenticate") == "" {
		t.Error("missing the WWW-Authenticate challenge itself")
	}
}
