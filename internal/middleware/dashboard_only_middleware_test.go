package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"expo-open-ota/internal/services"

	"github.com/gorilla/mux"
)

// runDashboardOnly sends a request through NewDashboardOnlyMiddleware with the
// context an upstream authentication middleware would have produced, which is
// the only input this gate reads.
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

// A publishing credential is app-scoped power over one app, not an account.
// The account surface is about the installation and the person signed into it,
// so it refuses one even though the credential is perfectly valid.
func TestDashboardOnlyRefusesAValidatedCliCredential(t *testing.T) {
	recorder := runDashboardOnly(t, func(r *http.Request) *http.Request {
		return r.WithContext(services.WithCliAuth(r.Context(), services.CliCredential{
			AppID: "test-app-id", KeyID: "key-1", KeyName: "ci",
		}))
	})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("a CLI credential must not reach the account surface, got %d", recorder.Code)
	}
}

// The same refusal, with a principal ALSO on the context so the missing-account
// check cannot be what produces it. NewAuthMiddleware stamps one or the other
// and never both, so this state does not occur in production: it is here
// because without it the test above passes whether or not the CLI check exists,
// and a test that cannot fail proves nothing about the branch it names.
func TestDashboardOnlyRefusesACliCredentialEvenBesideAPrincipal(t *testing.T) {
	recorder := runDashboardOnly(t, func(r *http.Request) *http.Request {
		ctx := services.WithPrincipal(r.Context(), &services.DashboardPrincipal{
			Email: "admin@expo-open-ota.dev", IsAdmin: true,
		})
		return r.WithContext(services.WithCliAuth(ctx, services.CliCredential{
			AppID: "test-app-id", KeyID: "key-1", KeyName: "ci",
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

// An empty context means the group was mounted without an authentication
// middleware in front of it. Refusing is the only safe reading of that, and it
// is what keeps this gate from failing open on a wiring mistake.
func TestDashboardOnlyRefusesAnUnauthenticatedContext(t *testing.T) {
	recorder := runDashboardOnly(t, func(r *http.Request) *http.Request { return r })
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("no credential at all must not fall through, got %d", recorder.Code)
	}
}
