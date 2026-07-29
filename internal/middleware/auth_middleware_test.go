package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"expo-open-ota/internal/database/postgres/pgdb"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/types"

	"github.com/gorilla/mux"
)

// runAuthMiddleware sends a request through NewAuthMiddleware on a
// non-app-scoped route, so the CLI-auth branch can only ever reject.
func runAuthMiddleware(t *testing.T, configure func(r *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	router := mux.NewRouter()
	router.Use(NewAuthMiddleware(services.NewDashboardAuthService(nil, nil), services.NewCliAuthService(nil)))
	router.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		if principal := services.PrincipalFromContext(r.Context()); principal != nil {
			w.Header().Set("X-Principal-Email", principal.Email)
			w.Header().Set("X-Principal-Admin", strconv.FormatBool(principal.IsAdmin))
		}
		w.Header().Set("X-Cli-App", services.CliAuthAppFromContext(r.Context()))
		w.WriteHeader(http.StatusOK)
	})
	r := httptest.NewRequest("GET", "/settings", nil)
	configure(r)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestAuthMiddleware(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("ADMIN_EMAIL", "admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "test-password")

	t.Run("cli auth rejected on a non-app-scoped route", func(t *testing.T) {
		w := runAuthMiddleware(t, func(r *http.Request) {
			r.Header.Set("Use-Cli-Auth", "true")
			r.Header.Set("Authorization", "Bearer some-cli-credential")
		})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for cli auth without app context, got %d", w.Code)
		}
	})

	t.Run("valid dashboard session is accepted", func(t *testing.T) {
		session, err := services.NewDashboardAuthService(nil, nil).LoginWithEmailPassword(context.Background(), "admin@example.com", "test-password")
		if err != nil {
			t.Fatalf("login failed: %v", err)
		}
		w := runAuthMiddleware(t, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+session.Token)
		})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 with valid session token, got %d", w.Code)
		}
		if got := w.Header().Get("X-Principal-Email"); got != "admin@example.com" {
			t.Fatalf("expected the principal email to reach the handler, got %q", got)
		}
		if got := w.Header().Get("X-Principal-Admin"); got != "true" {
			t.Fatalf("expected the stateless principal to be admin, got %q", got)
		}
		if got := w.Header().Get("X-Cli-App"); got != "" {
			t.Fatalf("a dashboard session must not carry the CLI marker, got %q", got)
		}
	})

	t.Run("invalid bearer token is rejected", func(t *testing.T) {
		w := runAuthMiddleware(t, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer not-a-jwt")
		})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 with invalid token, got %d", w.Code)
		}
	})

	t.Run("missing Authorization header is rejected", func(t *testing.T) {
		w := runAuthMiddleware(t, func(r *http.Request) {})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 with no Authorization header, got %d", w.Code)
		}
	})
}

// fakeCliAuthRepo accepts any credential and answers with key id 0.
type fakeCliAuthRepo struct{}

func (fakeCliAuthRepo) ValidateCliCredential(_ context.Context, _ string, _ types.Auth) (int64, error) {
	return 0, nil
}
func (fakeCliAuthRepo) InsertApiKey(_ context.Context, _ string, _ string, _ string, _ string) (int64, error) {
	return 0, nil
}
func (fakeCliAuthRepo) GetApiKeyNameByID(_ context.Context, _ string, _ int64) (string, error) {
	return "", nil
}

func (fakeCliAuthRepo) GetApiKeysMetadataByAppID(_ context.Context, _ string) ([]pgdb.GetApiKeysMetadataByAppIDRow, error) {
	return nil, nil
}
func (fakeCliAuthRepo) RevokeApiKeyByID(_ context.Context, _ int64, _ string) (string, error) {
	return "", nil
}

// TestAuthMiddlewareCliBranchStampsMarker checks that the CLI branch stamps
// the validated app on the context.
func TestAuthMiddlewareCliBranchStampsMarker(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")

	router := mux.NewRouter()
	appRouter := router.PathPrefix("/{APP_ID}").Subrouter()
	appRouter.Use(NewAuthMiddleware(services.NewDashboardAuthService(nil, nil), services.NewCliAuthService(fakeCliAuthRepo{})))
	appRouter.HandleFunc("/branches", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Cli-App", services.CliAuthAppFromContext(r.Context()))
		if services.PrincipalFromContext(r.Context()) != nil {
			t.Error("a CLI request must not carry a dashboard principal")
		}
		w.WriteHeader(http.StatusOK)
	})

	r := httptest.NewRequest("GET", "/my-app/branches", nil)
	r.Header.Set("Use-Cli-Auth", "true")
	r.Header.Set("Authorization", "Bearer eoo_some-api-key")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected the validated CLI request to pass, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Cli-App"); got != "my-app" {
		t.Fatalf("expected the CLI marker to carry the validated app id, got %q", got)
	}
}
