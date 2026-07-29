package infrastructure

import (
	"net/http"
	"xprem/internal/middleware"

	"github.com/gorilla/mux"
)

// The account-wide half of /api: everything that is about the installation
// itself rather than about one app, which is the account signed in, the
// edition it is running, and the administration of both. Called by the
// dashboard. Anything naming ONE app lives in routes_app.go; the app
// collection is the boundary case and it is here, at the end, because it names
// none and because routes_app.go opens a /apps/{APP_ID} prefix subrouter that
// has to be registered after it.
//
// AUTHENTICATION, in three layers.
//
// The first is on the /api subrouter, built in NewRouter:
// middleware.NewAuthMiddleware accepts one of two unrelated credentials,
// chosen by the Use-Cli-Auth header, and both travel as
// `Authorization: Bearer …`. Either way it resolves a principal and puts it on
// the request context. Nothing below runs without one.
//
// The second is this file's own, and it is why the routes below hang from a
// child subrouter rather than from /api directly: NewDashboardOnlyMiddleware
// refuses a CLI credential on everything registered here. A publishing token
// is app-scoped power, and nothing in this file is about one app.
//
// That child carries no path matcher, so it covers every route the file
// declares and nothing else: a request it does not match, /api/apps/{id}/...
// for instance, backtracks out and reaches routes_app.go without ever running
// this middleware.
//
// The third is per route, because admin and non-admin routes share path
// prefixes and a subrouter cannot separate them. adminOnly guards the global
// administration surface: users, roles, the license, SSO configuration and the
// audit log. The routes left ungated are the ones every signed-in account may
// read about itself or about the edition it is running.
func registerAccountRoutes(
	apiSubrouter *mux.Router,
	container *AppContainer,
	adminOnly mux.MiddlewareFunc,
) {
	accountSubrouter := apiSubrouter.NewRoute().Subrouter()
	accountSubrouter.Use(middleware.NewDashboardOnlyMiddleware())

	accountSubrouter.HandleFunc("/settings", container.SettingsHandler.GetSettingsHandler).Methods(http.MethodGet)

	// Current account
	accountSubrouter.HandleFunc("/me", container.UsersHandler.GetMeHandler).Methods(http.MethodGet)
	accountSubrouter.HandleFunc("/me/password", container.UsersHandler.ChangeMyPasswordHandler).Methods(http.MethodPut)

	// Enterprise license (control-plane only). Status is readable by every
	// signed-in account so the dashboard can reflect the edition; activating
	// or removing the key is admin-only.
	accountSubrouter.HandleFunc("/license", container.LicenseHandler.GetLicenseHandler).Methods(http.MethodGet)
	accountSubrouter.Handle("/license", adminOnly(http.HandlerFunc(container.LicenseHandler.ActivateLicenseHandler))).Methods(http.MethodPut)
	accountSubrouter.Handle("/license", adminOnly(http.HandlerFunc(container.LicenseHandler.RemoveLicenseHandler))).Methods(http.MethodDelete)

	// Enterprise SSO configuration (control-plane only, admin only), managed
	// from the dashboard's License page.
	accountSubrouter.Handle("/sso", adminOnly(http.HandlerFunc(container.SSOHandler.GetConfigHandler))).Methods(http.MethodGet)
	accountSubrouter.Handle("/sso", adminOnly(http.HandlerFunc(container.SSOHandler.SaveConfigHandler))).Methods(http.MethodPut)
	accountSubrouter.Handle("/sso", adminOnly(http.HandlerFunc(container.SSOHandler.DeleteConfigHandler))).Methods(http.MethodDelete)

	// Users management router (control-plane only, admin only)
	accountSubrouter.Handle("/users", adminOnly(http.HandlerFunc(container.UsersHandler.GetUsersHandler))).Methods(http.MethodGet)
	accountSubrouter.Handle("/users", adminOnly(http.HandlerFunc(container.UsersHandler.CreateUserHandler))).Methods(http.MethodPost)
	accountSubrouter.Handle("/users/{USER_ID}", adminOnly(http.HandlerFunc(container.UsersHandler.UpdateUserHandler))).Methods(http.MethodPatch)
	accountSubrouter.Handle("/users/{USER_ID}", adminOnly(http.HandlerFunc(container.UsersHandler.DeleteUserHandler))).Methods(http.MethodDelete)

	// Audit log (control-plane only, admin only). The log is append-only by
	// design, the retention purge being its single sanctioned exception;
	// reads are paginated and filterable.
	accountSubrouter.Handle("/audit/events", adminOnly(http.HandlerFunc(container.AuditHandler.ListAuditLogsHandler))).Methods(http.MethodGet)

	// Enterprise user roles & per-app grants (control-plane only). Managing
	// who can do what is an administration surface, so every route is
	// admin-only; the license gate lives in the service (reads work without a
	// license so the dashboard can show dormant grants, writes refuse).
	// /me/permissions is the one exception: every signed-in account may ask
	// what it is allowed to do — display support, the middlewares re-check
	// every mutation anyway.
	accountSubrouter.Handle("/roles", adminOnly(http.HandlerFunc(container.RBACHandler.ListRolesHandler))).Methods(http.MethodGet)
	accountSubrouter.Handle("/roles", adminOnly(http.HandlerFunc(container.RBACHandler.CreateRoleHandler))).Methods(http.MethodPost)
	accountSubrouter.Handle("/roles/{ROLE_ID}", adminOnly(http.HandlerFunc(container.RBACHandler.UpdateRoleHandler))).Methods(http.MethodPut)
	accountSubrouter.Handle("/roles/{ROLE_ID}", adminOnly(http.HandlerFunc(container.RBACHandler.DeleteRoleHandler))).Methods(http.MethodDelete)
	accountSubrouter.Handle("/users/{USER_ID}/grants", adminOnly(http.HandlerFunc(container.RBACHandler.GetUserGrantsHandler))).Methods(http.MethodGet)
	accountSubrouter.Handle("/users/{USER_ID}/grants", adminOnly(http.HandlerFunc(container.RBACHandler.SetUserGrantsHandler))).Methods(http.MethodPut)
	accountSubrouter.Handle("/users/grants/summary", adminOnly(http.HandlerFunc(container.RBACHandler.GetGrantSummaryHandler))).Methods(http.MethodGet)
	accountSubrouter.HandleFunc("/me/permissions", container.RBACHandler.GetMyPermissionsHandler).Methods(http.MethodGet)
	accountSubrouter.HandleFunc("/apps", container.AppHandler.GetAppsHandler).Methods(http.MethodGet)
	accountSubrouter.Handle("/apps", adminOnly(http.HandlerFunc(container.AppHandler.CreateAppHandler))).Methods(http.MethodPost)

}
