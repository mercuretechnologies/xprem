package infrastructure

import (
	"expo-open-ota/ee/rbac"
	"net/http"

	"github.com/gorilla/mux"
)

// The account-wide half of /api: everything that is about the installation
// itself rather than about one app. Called by the dashboard.
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
// administration surface: users, roles, the license, SSO configuration, the
// audit log and app creation. The routes left ungated are the ones every
// signed-in account may read about itself or about the edition it is running.
//
// The flat /apps routes at the end are here rather than in routes_app.go for a
// routing reason and it is worth stating: routes_app.go registers a PathPrefix
// subrouter on /apps/{APP_ID} with StrictSlash(true), so these have to be
// registered BEFORE it or the prefix would claim them first and the trailing
// slash rule would answer with a redirect.
func registerAccountRoutes(
	apiSubrouter *mux.Router,
	container *AppContainer,
	adminOnly mux.MiddlewareFunc,
	requirePermission func(rbac.Permission) mux.MiddlewareFunc,
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

	// Apps management router. Creating an app is global administration and
	// stays admin-only; acting on an existing app is permission-gated.
	apiSubrouter.Handle("/apps", adminOnly(http.HandlerFunc(container.AppHandler.CreateAppHandler))).Methods(http.MethodPost)
	apiSubrouter.Handle("/apps/{APP_ID}", requirePermission(rbac.PermAppDelete)(http.HandlerFunc(container.AppHandler.DeleteAppHandler))).Methods(http.MethodDelete)
	apiSubrouter.Handle("/apps/{APP_ID}", requirePermission(rbac.PermAppRename)(http.HandlerFunc(container.AppHandler.UpdateAppHandler))).Methods(http.MethodPatch)
	apiSubrouter.HandleFunc("/apps", container.AppHandler.GetAppsHandler).Methods(http.MethodGet)
	// The signing certificate is key material — admins, or the explicit
	// certificate:read permission.
	apiSubrouter.Handle("/apps/{APP_ID}/certificate", requirePermission(rbac.PermCertificateRead)(http.HandlerFunc(container.AppHandler.DownloadAppCertificateHandler))).Methods(http.MethodGet)
}
