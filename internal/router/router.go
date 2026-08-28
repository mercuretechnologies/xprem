package infrastructure

import (
	"net/http"
	"xprem/ee/geoip"
	"xprem/internal/middleware"

	"github.com/gorilla/mux"
)

func HealthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// branchVarName/branchVar is the only path variable a publishing token may be
// scoped on.
const (
	branchVarName = "BRANCH"
	branchVar     = "{" + branchVarName + "}"
)

// NewRouter builds the whole routing table, one registerX call per audience:
// infra, publish, ingest, client, pre-auth, dashboard assets, then the
// authenticated account and app routes.
func NewRouter(container *AppContainer) *mux.Router {
	r := mux.NewRouter()
	// SkipClean(true): a double slash or dot segment must 404, not redirect.
	// A 301 on a non-GET method gets rewritten to GET by callers that follow
	// redirects, which would turn e.g. a failed DELETE into an apparent success.
	r.SkipClean(true)
	r.Use(middleware.LoggingMiddleware)
	r.Use(middleware.RequestMetaMiddleware)
	if geoMiddleware := geoip.GetMiddleware(); geoMiddleware != nil {
		r.Use(geoMiddleware)
	}

	// No authentication below this point.
	registerInfraRoutes(r)
	registerMCPRoutes(r, container)
	registerPublishRoutes(r, container)
	registerIngestRoutes(r, container)
	registerClientRoutes(r, container)
	registerPreAuthRoutes(r, container)
	registerOAuthRoutes(r, container)
	registerDashboardAssets(r)

	// Authentication from here on: the Use-Cli-Auth header picks between a
	// CLI credential and the dashboard session JWT.
	apiSubrouter := r.PathPrefix("/api").Subrouter()
	// CORS first: a preflight carries no Authorization header and must be
	// answered before the auth middleware; the catch-all makes it match.
	apiSubrouter.Use(middleware.NewDashboardCORSMiddleware())
	apiSubrouter.PathPrefix("/").HandlerFunc(func(http.ResponseWriter, *http.Request) {}).Methods(http.MethodOptions)
	apiSubrouter.Use(middleware.NewAuthMiddleware(container.DashboardAuthService, container.CliAuthService))

	// adminOnly guards the global administration surface (users, roles,
	// license, SSO, app creation).
	adminOnly := middleware.NewAdminMiddleware(container.UserRepo)

	registerOAuthApiRoutes(apiSubrouter, container)
	registerAccountRoutes(apiSubrouter, container, adminOnly)
	registerAppRoutes(apiSubrouter, container)

	return mountPublicPath(r, container)
}
