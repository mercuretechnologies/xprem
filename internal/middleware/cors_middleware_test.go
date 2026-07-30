package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

func dashboardCORSRouter(t *testing.T) *mux.Router {
	t.Helper()
	t.Setenv("BASE_URL", "https://ota.example.com")
	router := mux.NewRouter()
	sub := router.PathPrefix("/api").Subrouter()
	sub.Use(NewDashboardCORSMiddleware())
	sub.PathPrefix("/").HandlerFunc(func(http.ResponseWriter, *http.Request) {}).Methods(http.MethodOptions)
	sub.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodGet)
	return router
}

func TestDashboardCORSAllowedOrigins(t *testing.T) {
	router := dashboardCORSRouter(t)
	for _, origin := range []string{"https://ota.example.com", "http://localhost:5173", "http://127.0.0.1:4000"} {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodOptions, "/api/me", nil)
		req.Header.Set("Origin", origin)
		router.ServeHTTP(res, req)

		if res.Code != http.StatusNoContent {
			t.Fatalf("%s: expected 204 on preflight, got %d", origin, res.Code)
		}
		if res.Header().Get("Access-Control-Allow-Origin") != origin {
			t.Errorf("%s: expected the origin echoed back, got %q", origin, res.Header().Get("Access-Control-Allow-Origin"))
		}
	}
}

func TestDashboardCORSForeignOriginRefused(t *testing.T) {
	router := dashboardCORSRouter(t)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	router.ServeHTTP(res, req)

	if res.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("a foreign origin must get no CORS headers")
	}
	// The request itself still runs; CORS only governs what the calling page
	// may read, authentication is the auth middleware's job.
	if res.Code != http.StatusOK {
		t.Fatalf("expected the request to reach the handler, got %d", res.Code)
	}
}

func TestDashboardCORSNoOrigin(t *testing.T) {
	router := dashboardCORSRouter(t)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/me", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}
	if res.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("no Origin, no CORS headers")
	}
}
