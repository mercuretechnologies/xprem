package middleware

import (
	"net/http"
	"net/url"
	"xprem/config"

	"github.com/gorilla/mux"
)

// NewDashboardCORSMiddleware serves the only browser that legitimately calls
// the dashboard surfaces (/api, /auth) cross-origin: the SPA on a localhost
// dev server. In production the SPA is served by this server under /dashboard,
// same origin, and no other website gets to read these responses from a
// visitor's browser. Preflights are answered here, before authentication,
// because they carry no Authorization header; the subrouter needs a catch-all
// OPTIONS route for them to match.
func NewDashboardCORSMiddleware() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && isDashboardOrigin(origin) {
				w.Header().Add("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Use-Cli-Auth")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isDashboardOrigin accepts the deployment's own origin and local dev servers
// (vite serves the SPA from localhost on an arbitrary port).
func isDashboardOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	hostname := parsed.Hostname()
	if hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" {
		return true
	}
	base, err := url.Parse(config.BaseURL())
	if err != nil {
		return false
	}
	return parsed.Scheme == base.Scheme && parsed.Host == base.Host
}
