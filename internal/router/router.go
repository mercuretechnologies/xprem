package infrastructure

import (
	"expo-open-ota/ee/rbac"
	"expo-open-ota/internal/middleware"
	"net/http"

	"github.com/gorilla/mux"
)

func HealthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// NewRouter is the whole routing table in the order it is matched, one line
// per audience. Each registerX lives in its own routes_*.go, and each of those
// files opens with what its routes are for and what authentication stands in
// front of them, because "who may call this" is the question a routing table
// is read to answer and it was previously spread over three hundred lines.
//
// The audiences, and it is worth knowing which is which before reading any of
// them: the orchestrator and the metrics scraper (infra), the eoas CLI
// publishing an update (publish), the expo-observe SDK inside every installed
// app (ingest), expo-updates inside those same apps (client), a browser
// signing in (pre-auth) and then loading the front-end (dashboard assets), and
// finally the signed-in dashboard itself (account, app).
//
// Only the last two sit behind an authentication middleware. The ones before
// them are reachable without a credential, which is a property of what calls
// them rather than an omission: a liveness probe, an installed mobile app and
// a login page have no credential to present. The publish group is the odd one
// out and its file says why, the check being inside each handler because what
// is authorised is a branch and not just an app.
//
// The ORDER of these calls is load-bearing in one place, noted again in
// routes_account.go: the flat /api/apps routes are registered before the
// /apps/{APP_ID} prefix subrouter that routes_app.go opens, which is what
// keeps a DELETE on /api/apps/{id} from meeting that subrouter's StrictSlash
// rule. Everything else matches on disjoint paths.
//
// This package is also the composition root, so it is the one place under
// internal/ that reaches into ee/. Enterprise behavior is wired here and
// nowhere below.
func NewRouter(container *AppContainer) *mux.Router {
	r := mux.NewRouter()
	r.Use(middleware.LoggingMiddleware)
	// Every request carries its network context (client IP, user agent) so
	// audit events can be emitted from any layer below without the request.
	r.Use(middleware.RequestMetaMiddleware)

	// No authentication below this point.
	registerInfraRoutes(r)
	registerPublishRoutes(r, container)
	registerIngestRoutes(r, container)
	registerClientRoutes(r, container)
	registerPreAuthRoutes(r, container)
	registerDashboardAssets(r)

	// Authentication from here on. The middleware guards a route with one of
	// two unrelated credentials, picked by the Use-Cli-Auth header: a CLI
	// credential scoped to an app, or the dashboard's own session JWT. Both
	// travel as `Authorization: Bearer …`, which is why the header decides
	// which one to expect rather than the credential's shape.
	apiSubrouter := r.PathPrefix("/api").Subrouter()
	apiSubrouter.Use(middleware.NewAuthMiddleware(container.DashboardAuthService, container.CliAuthService))

	// Two gates share the mutation routes below. adminOnly guards the global
	// administration surface (users, roles, license, SSO, app creation).
	// requirePermission guards the app-scoped mutations: admins always pass,
	// members need the permission on the route's app through their enterprise
	// grants (ee/rbac), and without a control plane or a valid license it
	// degrades to exactly adminOnly's behavior, keeping members read-only.
	// Both wrap individual routes rather than a subrouter because admin and
	// non-admin routes share path prefixes.
	adminOnly := middleware.NewAdminMiddleware(container.UserRepo)
	requirePermission := func(perm rbac.Permission) mux.MiddlewareFunc {
		return rbac.RequirePermission(container.RBACService, perm)
	}

	registerAccountRoutes(apiSubrouter, container, adminOnly, requirePermission)
	registerAppRoutes(apiSubrouter, container, requirePermission)

	return r
}
