package infrastructure

import (
	"net/http"
	"strings"
	"xprem/config"

	"github.com/gorilla/mux"
)

// mountPublicPath serves the app under BASE_URL's path when
// SERVE_FROM_SUB_PATH is true; by default the reverse proxy is expected to
// strip the prefix. /hc and /ready stay at the process root either way.
func mountPublicPath(inner *mux.Router, container *AppContainer) *mux.Router {
	prefix := config.PublicPath()
	if prefix == "" || !config.ServeFromSubPath() {
		return inner
	}
	outer := mux.NewRouter()
	outer.SkipClean(true)
	registerInfraRoutes(outer)
	registerOAuthWellKnownInserted(outer, container, prefix)
	stripped := stripPrefix(prefix, inner)
	outer.Handle(prefix, stripped)
	outer.PathPrefix(prefix + "/").Handler(stripped)
	return outer
}

func stripPrefix(prefix string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, prefix)
		if p == r.URL.Path {
			http.NotFound(w, r)
			return
		}
		if p == "" {
			p = "/"
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = p
		if r.URL.RawPath != "" {
			r2.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, prefix)
		}
		next.ServeHTTP(w, r2)
	})
}
