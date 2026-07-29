package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"expo-open-ota/internal/services"

	"github.com/gorilla/mux"
)

// runDashboardOnly sends a request through NewDashboardOnlyMiddleware with the
// context an upstream authentication middleware would have produced.
func runDashboardOnly(t *testing.T, stamp func(*http.Request) *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	router := mux.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, stamp(r))
		})
	})
	router.Use(NewDashboardOnlyMiddleware())
	router.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodGet)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/settings", nil))
	return recorder
}

// TestDashboardOnlyRefusesAValidatedCliCredential checks that a valid CLI
// credential is still refused on the account surface.
func TestDashboardOnlyRefusesAValidatedCliCredential(t *testing.T) {
	recorder := runDashboardOnly(t, func(r *http.Request) *http.Request {
		return r.WithContext(services.WithCliAuth(r.Context(), services.CliCredential{
			AppID: "test-app-id", KeyID: 1, KeyName: "ci",
		}))
	})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("a CLI credential must not reach the account surface, got %d", recorder.Code)
	}
}

// TestDashboardOnlyRefusesACliCredentialEvenBesideAPrincipal checks that the
// CLI credential itself is what triggers the refusal, with a principal also
// on the context so the missing-account check cannot be the cause instead.
func TestDashboardOnlyRefusesACliCredentialEvenBesideAPrincipal(t *testing.T) {
	recorder := runDashboardOnly(t, func(r *http.Request) *http.Request {
		ctx := services.WithPrincipal(r.Context(), &services.DashboardPrincipal{
			Email: "admin@expo-open-ota.dev", IsAdmin: true,
		})
		return r.WithContext(services.WithCliAuth(ctx, services.CliCredential{
			AppID: "test-app-id", KeyID: 1, KeyName: "ci",
		}))
	})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("the CLI credential itself must be what refuses, got %d", recorder.Code)
	}
}

func TestDashboardOnlyLetsASignedInAccountThrough(t *testing.T) {
	recorder := runDashboardOnly(t, func(r *http.Request) *http.Request {
		return r.WithContext(services.WithPrincipal(r.Context(), &services.DashboardPrincipal{
			Email: "admin@expo-open-ota.dev", IsAdmin: true,
		}))
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("a dashboard session is exactly what this surface is for, got %d", recorder.Code)
	}
}

// TestDashboardOnlyRefusesAnUnauthenticatedContext checks that a request with
// no credential at all is refused rather than let through.
func TestDashboardOnlyRefusesAnUnauthenticatedContext(t *testing.T) {
	recorder := runDashboardOnly(t, func(r *http.Request) *http.Request { return r })
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("no credential at all must not fall through, got %d", recorder.Code)
	}
}
