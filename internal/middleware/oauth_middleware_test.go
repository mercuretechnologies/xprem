package middleware

import (
	"context"
	"errors"
	"expo-open-ota/internal/crypto"
	"expo-open-ota/internal/oauth"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

type fakeOAuthUserRepo struct {
	user store.User
	err  error
}

func (f *fakeOAuthUserRepo) GetUserByID(_ context.Context, id string) (store.User, error) {
	if f.err != nil {
		return store.User{}, f.err
	}
	if f.user.Id != id {
		return store.User{}, &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	return f.user, nil
}

func (f *fakeOAuthUserRepo) BumpUserSessionVersion(_ context.Context, _ string) error {
	return nil
}

// principalCapture records what the middleware put in the context of the
// request that reached the wrapped handler.
type principalCapture struct {
	principal *services.DashboardPrincipal
}

func oauthTestSetup(t *testing.T, repo *fakeOAuthUserRepo) (*oauth.OAuthService, http.Handler, *principalCapture) {
	t.Helper()
	t.Setenv("BASE_URL", "https://ota.example.com")
	t.Setenv("JWT_SECRET", "test-secret")
	service := oauth.NewOAuthService(nil, nil, nil, repo)

	capture := &principalCapture{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.principal = services.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	return service, NewOAuthMiddleware(service)(next), capture
}

func doMCPRequest(handler http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func enabledUser() store.User {
	return store.User{Id: "user-1", Email: "a@b.c", IsAdmin: false, Enabled: true, SessionVersion: 3}
}

func TestOAuthMiddlewareMissingToken(t *testing.T) {
	_, handler, _ := oauthTestSetup(t, &fakeOAuthUserRepo{user: enabledUser()})
	res := doMCPRequest(handler, "")

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.Code)
	}
	challenge := res.Header().Get("WWW-Authenticate")
	if !strings.Contains(challenge, `resource_metadata="https://ota.example.com/.well-known/oauth-protected-resource/mcp"`) {
		t.Errorf("WWW-Authenticate must point at the resource metadata, got %q", challenge)
	}
}

func TestOAuthMiddlewareValidToken(t *testing.T) {
	repo := &fakeOAuthUserRepo{user: enabledUser()}
	service, handler, capture := oauthTestSetup(t, repo)
	token, err := service.IssueAccessToken(services.DashboardPrincipal{UserId: "user-1", SessionVersion: 3})
	if err != nil {
		t.Fatal(err)
	}

	res := doMCPRequest(handler, token)
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body.String())
	}
	if capture.principal == nil {
		t.Fatal("no principal in the request context")
	}
	// The principal must come from the user row, not from the claims.
	if capture.principal.Email != "a@b.c" || capture.principal.UserId != "user-1" {
		t.Errorf("unexpected principal: %+v", capture.principal)
	}
}

func TestOAuthMiddlewareRejections(t *testing.T) {
	repo := &fakeOAuthUserRepo{user: enabledUser()}
	service, handler, _ := oauthTestSetup(t, repo)

	dashboardToken, _ := crypto.GenerateJWTToken("test-secret", jwt.MapClaims{
		"sub": "admin-dashboard", "type": "token", "userId": "user-1", "sv": 3,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	expiredToken, _ := crypto.GenerateJWTToken("test-secret", jwt.MapClaims{
		"sub": "mcp", "aud": "https://ota.example.com/mcp", "userId": "user-1", "sv": 3,
		"exp": time.Now().Add(-time.Minute).Unix(),
	})
	wrongAudience, _ := crypto.GenerateJWTToken("test-secret", jwt.MapClaims{
		"sub": "mcp", "aud": "https://other.example.com/mcp", "userId": "user-1", "sv": 3,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	staleSessionVersion, err := service.IssueAccessToken(services.DashboardPrincipal{UserId: "user-1", SessionVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	unknownUser, err := service.IssueAccessToken(services.DashboardPrincipal{UserId: "user-2", SessionVersion: 3})
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"dashboard token":       dashboardToken,
		"expired token":         expiredToken,
		"wrong audience":        wrongAudience,
		"stale session version": staleSessionVersion,
		"unknown user":          unknownUser,
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			res := doMCPRequest(handler, token)
			if res.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", res.Code)
			}
			if challenge := res.Header().Get("WWW-Authenticate"); !strings.Contains(challenge, "resource_metadata") {
				t.Errorf("the 401 must carry the discovery challenge, got %q", challenge)
			}
		})
	}
}

func TestOAuthMiddlewareDisabledUser(t *testing.T) {
	user := enabledUser()
	user.Enabled = false
	repo := &fakeOAuthUserRepo{user: user}
	service, handler, _ := oauthTestSetup(t, repo)
	token, err := service.IssueAccessToken(services.DashboardPrincipal{UserId: "user-1", SessionVersion: 3})
	if err != nil {
		t.Fatal(err)
	}

	if res := doMCPRequest(handler, token); res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.Code)
	}
}

func TestOAuthMiddlewareDatabaseOutage(t *testing.T) {
	repo := &fakeOAuthUserRepo{err: errors.New("connection refused")}
	service, handler, _ := oauthTestSetup(t, repo)
	token, err := service.IssueAccessToken(services.DashboardPrincipal{UserId: "user-1", SessionVersion: 3})
	if err != nil {
		t.Fatal(err)
	}

	res := doMCPRequest(handler, token)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("an outage must be a 500, not a dead token: got %d", res.Code)
	}
	if res.Header().Get("WWW-Authenticate") != "" {
		t.Error("a 500 must not carry WWW-Authenticate, the client would restart the flow")
	}
	if strings.Contains(res.Body.String(), "connection refused") {
		t.Error("the response body must not leak the underlying database error")
	}
}

func TestOAuthMiddlewareBindsSessionUser(t *testing.T) {
	repo := &fakeOAuthUserRepo{user: enabledUser()}
	service, _, _ := oauthTestSetup(t, repo)
	token, err := service.IssueAccessToken(services.DashboardPrincipal{UserId: "user-1", SessionVersion: 3})
	if err != nil {
		t.Fatal(err)
	}

	// The streamable transport pins sessions to TokenInfo.UserID; assert the
	// middleware actually plants it where the SDK reads it.
	var seenInfo *auth.TokenInfo
	handler := NewOAuthMiddleware(service)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenInfo = auth.TokenInfoFromContext(r.Context())
	}))
	doMCPRequest(handler, token)
	if seenInfo == nil || seenInfo.UserID != "user-1" {
		t.Fatalf("TokenInfo.UserID must reach the transport, got %+v", seenInfo)
	}
}
