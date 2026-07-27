package infrastructure

import (
	"net/http"

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
// AUTHENTICATION, in two layers.
//
// The first is on the /api subrouter these routes hang from, built in
// NewRouter: middleware.NewAuthMiddleware accepts one of two unrelated
// credentials, chosen by the Use-Cli-Auth header, and both travel as
// `Authorization: Bearer …`. Either way it resolves a principal and puts it on
// the request context. Nothing below runs without one.
//
// The second is per route, because admin and non-admin routes share path
// prefixes and a subrouter cannot separate them. adminOnly guards the global
// administration surface: users, roles, the license, SSO configuration and the
// audit log. The routes left ungated are the ones every signed-in account may
// read about itself or about the edition it is running.
func registerAccountRoutes(
	apiSubrouter *mux.Router,
	container *AppContainer,
	adminOnly mux.MiddlewareFunc,
) {
	apiSubrouter.HandleFunc("/settings", container.SettingsHandler.GetSettingsHandler).Methods(http.MethodGet)

	// Current account
	apiSubrouter.HandleFunc("/me", container.UsersHandler.GetMeHandler).Methods(http.MethodGet)
	apiSubrouter.HandleFunc("/me/password", container.UsersHandler.ChangeMyPasswordHandler).Methods(http.MethodPut)

	// Enterprise license (control-plane only). Status is readable by every
	// signed-in account so the dashboard can reflect the edition; activating
	// or removing the key is admin-only.
	apiSubrouter.HandleFunc("/license", container.LicenseHandler.GetLicenseHandler).Methods(http.MethodGet)
	apiSubrouter.Handle("/license", adminOnly(http.HandlerFunc(container.LicenseHandler.ActivateLicenseHandler))).Methods(http.MethodPut)
	apiSubrouter.Handle("/license", adminOnly(http.HandlerFunc(container.LicenseHandler.RemoveLicenseHandler))).Methods(http.MethodDelete)

	// Enterprise SSO configuration (control-plane only, admin only), managed
	// from the dashboard's License page.
	apiSubrouter.Handle("/sso", adminOnly(http.HandlerFunc(container.SSOHandler.GetConfigHandler))).Methods(http.MethodGet)
	apiSubrouter.Handle("/sso", adminOnly(http.HandlerFunc(container.SSOHandler.SaveConfigHandler))).Methods(http.MethodPut)
	apiSubrouter.Handle("/sso", adminOnly(http.HandlerFunc(container.SSOHandler.DeleteConfigHandler))).Methods(http.MethodDelete)

	// Users management router (control-plane only, admin only)
	apiSubrouter.Handle("/users", adminOnly(http.HandlerFunc(container.UsersHandler.GetUsersHandler))).Methods(http.MethodGet)
	apiSubrouter.Handle("/users", adminOnly(http.HandlerFunc(container.UsersHandler.CreateUserHandler))).Methods(http.MethodPost)
	apiSubrouter.Handle("/users/{USER_ID}", adminOnly(http.HandlerFunc(container.UsersHandler.UpdateUserHandler))).Methods(http.MethodPatch)
	apiSubrouter.Handle("/users/{USER_ID}", adminOnly(http.HandlerFunc(container.UsersHandler.DeleteUserHandler))).Methods(http.MethodDelete)

	// Audit log (control-plane only, admin only). The log is append-only by
	// design, the retention purge being its single sanctioned exception;
	// reads are paginated and filterable.
	apiSubrouter.Handle("/audit/events", adminOnly(http.HandlerFunc(container.AuditHandler.ListAuditLogsHandler))).Methods(http.MethodGet)

	// Enterprise user roles & per-app grants (control-plane only). Managing
	// who can do what is an administration surface, so every route is
	// admin-only; the license gate lives in the service (reads work without a
	// license so the dashboard can show dormant grants, writes refuse).
	// /me/permissions is the one exception: every signed-in account may ask
	// what it is allowed to do — display support, the middlewares re-check
	// every mutation anyway.
	apiSubrouter.Handle("/roles", adminOnly(http.HandlerFunc(container.RBACHandler.ListRolesHandler))).Methods(http.MethodGet)
	apiSubrouter.Handle("/roles", adminOnly(http.HandlerFunc(container.RBACHandler.CreateRoleHandler))).Methods(http.MethodPost)
	apiSubrouter.Handle("/roles/{ROLE_ID}", adminOnly(http.HandlerFunc(container.RBACHandler.UpdateRoleHandler))).Methods(http.MethodPut)
	apiSubrouter.Handle("/roles/{ROLE_ID}", adminOnly(http.HandlerFunc(container.RBACHandler.DeleteRoleHandler))).Methods(http.MethodDelete)
	apiSubrouter.Handle("/users/{USER_ID}/grants", adminOnly(http.HandlerFunc(container.RBACHandler.GetUserGrantsHandler))).Methods(http.MethodGet)
	apiSubrouter.Handle("/users/{USER_ID}/grants", adminOnly(http.HandlerFunc(container.RBACHandler.SetUserGrantsHandler))).Methods(http.MethodPut)
	apiSubrouter.Handle("/users/grants/summary", adminOnly(http.HandlerFunc(container.RBACHandler.GetGrantSummaryHandler))).Methods(http.MethodGet)
	apiSubrouter.HandleFunc("/me/permissions", container.RBACHandler.GetMyPermissionsHandler).Methods(http.MethodGet)
	apiSubrouter.HandleFunc("/apps", container.AppHandler.GetAppsHandler).Methods(http.MethodGet)
	apiSubrouter.Handle("/apps", adminOnly(http.HandlerFunc(container.AppHandler.CreateAppHandler))).Methods(http.MethodPost)

}
