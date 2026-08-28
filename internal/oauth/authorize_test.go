package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"xprem/internal/services"
	"xprem/internal/store"

	"github.com/google/uuid"
)

// A well-formed S256 challenge is 43 chars of base64url.
const testChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

func seedClient(t *testing.T, repo *fakeClientRepo, redirectURIs ...string) string {
	t.Helper()
	id := uuid.New().String()
	repo.inserted = append(repo.inserted, store.InsertOAuthClientParameters{
		ID:           id,
		Name:         "Claude Code",
		RedirectURIs: redirectURIs,
	})
	return id
}

func validAuthorizeQuery(clientID string) url.Values {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", "http://127.0.0.1:53422/callback")
	q.Set("response_type", "code")
	q.Set("code_challenge", testChallenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", "xyz")
	q.Set("resource", "https://ota.example.com/mcp")
	return q
}

func TestAuthorizeRedirectsToConsent(t *testing.T) {
	handler, clientRepo, _ := newTestHandlerWithCodes(t)
	clientID := seedClient(t, clientRepo, "http://127.0.0.1:53422/callback")

	res := httptest.NewRecorder()
	handler.AuthorizeHandler(res, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+validAuthorizeQuery(clientID).Encode(), nil))

	if res.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", res.Code, res.Body.String())
	}
	location, err := url.Parse(res.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(location.String(), "https://ota.example.com/dashboard/oauth/consent?") {
		t.Fatalf("expected a consent bounce, got %s", location)
	}
	forwarded := location.Query()
	if forwarded.Get("client_name") != "Claude Code" {
		t.Error("the consent screen needs the server-resolved client_name")
	}
	if forwarded.Get("state") != "xyz" || forwarded.Get("code_challenge") != testChallenge {
		t.Error("state and code_challenge must survive the bounce")
	}
}

func TestAuthorizeConsentURLKeepsBasePath(t *testing.T) {
	handler, clientRepo, _ := newTestHandlerWithCodes(t)
	t.Setenv("BASE_URL", "https://api.example.com/ota")
	clientID := seedClient(t, clientRepo, "http://127.0.0.1:53422/callback")

	query := validAuthorizeQuery(clientID)
	query.Set("resource", "https://api.example.com/ota/mcp")
	res := httptest.NewRecorder()
	handler.AuthorizeHandler(res, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+query.Encode(), nil))

	if res.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", res.Code, res.Body.String())
	}
	location := res.Header().Get("Location")
	if !strings.HasPrefix(location, "https://api.example.com/ota/dashboard/oauth/consent?") {
		t.Fatalf("expected consent under BASE_URL path, got %s", location)
	}
}

func TestAuthorizeRejections(t *testing.T) {
	handler, clientRepo, _ := newTestHandlerWithCodes(t)
	clientID := seedClient(t, clientRepo, "http://127.0.0.1:53422/callback")

	mutations := map[string]func(url.Values){
		"unknown client":         func(q url.Values) { q.Set("client_id", uuid.New().String()) },
		"malformed client id":    func(q url.Values) { q.Set("client_id", "not-a-uuid") },
		"unregistered redirect":  func(q url.Values) { q.Set("redirect_uri", "https://evil.example.com/cb") },
		"wrong response_type":    func(q url.Values) { q.Set("response_type", "token") },
		"missing code_challenge": func(q url.Values) { q.Del("code_challenge") },
		"plain challenge method": func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"unsupported scope":      func(q url.Values) { q.Set("scope", "admin") },
		"foreign resource":       func(q url.Values) { q.Set("resource", "https://other.example.com/mcp") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			q := validAuthorizeQuery(clientID)
			mutate(q)
			res := httptest.NewRecorder()
			handler.AuthorizeHandler(res, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))

			if res.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", res.Code)
			}
			if location := res.Header().Get("Location"); location != "" {
				t.Fatalf("an invalid request must never redirect, got Location %q", location)
			}
		})
	}
}

func TestConsentMintsCode(t *testing.T) {
	handler, clientRepo, codeRepo := newTestHandlerWithCodes(t)
	clientID := seedClient(t, clientRepo, "http://127.0.0.1:53422/callback")

	form := validAuthorizeQuery(clientID)
	form.Set("decision", "approve")
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/consent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(services.WithPrincipal(context.Background(), &services.DashboardPrincipal{UserId: "user-1"}))
	res := httptest.NewRecorder()
	handler.ConsentHandler(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body.String())
	}
	body := decodeJSON(t, res)
	redirectURL, err := url.Parse(body["redirectUrl"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if redirectURL.Host != "127.0.0.1:53422" || redirectURL.Path != "/callback" {
		t.Fatalf("the code must be delivered to the registered redirect_uri, got %s", redirectURL)
	}
	code := redirectURL.Query().Get("code")
	if code == "" || redirectURL.Query().Get("state") != "xyz" {
		t.Fatalf("expected code and state in the redirect, got %s", redirectURL)
	}
	if len(codeRepo.inserted) != 1 {
		t.Fatalf("expected 1 stored code, got %d", len(codeRepo.inserted))
	}
	stored := codeRepo.inserted[0]
	if stored.ID != code || stored.UserID != "user-1" || stored.ClientID != clientID || stored.CodeChallenge != testChallenge {
		t.Errorf("stored code does not freeze the consent context: %+v", stored)
	}
	if remaining := time.Until(stored.ExpiresAt); remaining <= 0 || remaining > 2*time.Minute {
		t.Errorf("unexpected code TTL: %v", remaining)
	}
}

func TestConsentDeny(t *testing.T) {
	handler, clientRepo, codeRepo := newTestHandlerWithCodes(t)
	clientID := seedClient(t, clientRepo, "http://127.0.0.1:53422/callback")

	form := validAuthorizeQuery(clientID)
	form.Set("decision", "deny")
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/consent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(services.WithPrincipal(context.Background(), &services.DashboardPrincipal{UserId: "user-1"}))
	res := httptest.NewRecorder()
	handler.ConsentHandler(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body.String())
	}
	body := decodeJSON(t, res)
	redirectURL, err := url.Parse(body["redirectUrl"].(string))
	if err != nil {
		t.Fatal(err)
	}
	query := redirectURL.Query()
	if query.Get("error") != "access_denied" || query.Get("state") != "xyz" || query.Get("code") != "" {
		t.Fatalf("a denial must deliver error=access_denied and no code, got %s", redirectURL)
	}
	if len(codeRepo.inserted) != 0 {
		t.Error("a denial must not mint a code")
	}
}

func TestConsentWithoutPrincipal(t *testing.T) {
	handler, clientRepo, _ := newTestHandlerWithCodes(t)
	clientID := seedClient(t, clientRepo, "http://127.0.0.1:53422/callback")

	form := validAuthorizeQuery(clientID)
	form.Set("decision", "approve")
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/consent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res := httptest.NewRecorder()
	handler.ConsentHandler(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.Code)
	}
}
