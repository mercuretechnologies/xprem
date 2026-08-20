package infrastructure

import (
	"net/http"
	"xprem/internal/middleware"

	"github.com/gorilla/mux"
)

// registerAccountRoutes registers the account-wide half of /api: the signed-in
// account, the edition it is running, and the administration of both. Routes
// naming one app live in routes_app.go instead.
func registerAccountRoutes(
	apiSubrouter *mux.Router,
	container *AppContainer,
	adminOnly mux.MiddlewareFunc,
) {
	accountSubrouter := apiSubrouter.NewRoute().Subrouter()
	accountSubrouter.Use(middleware.NewDashboardOnlyMiddleware())

	accountSubrouter.HandleFunc("/settings", container.SettingsHandler.GetSettingsHandler).Methods(http.MethodGet)

	accountSubrouter.HandleFunc("/me", container.UsersHandler.GetMeHandler).Methods(http.MethodGet)
	accountSubrouter.HandleFunc("/me/password", container.UsersHandler.ChangeMyPasswordHandler).Methods(http.MethodPut)

	accountSubrouter.HandleFunc("/license", container.LicenseHandler.GetLicenseHandler).Methods(http.MethodGet)
	accountSubrouter.Handle("/license/check", adminOnly(http.HandlerFunc(container.LicenseHandler.CheckLicenseHandler))).Methods(http.MethodPost)
	accountSubrouter.Handle("/license", adminOnly(http.HandlerFunc(container.LicenseHandler.ActivateLicenseHandler))).Methods(http.MethodPut)
	accountSubrouter.Handle("/license", adminOnly(http.HandlerFunc(container.LicenseHandler.RemoveLicenseHandler))).Methods(http.MethodDelete)

	accountSubrouter.Handle("/sso", adminOnly(http.HandlerFunc(container.SSOHandler.GetConfigHandler))).Methods(http.MethodGet)
	accountSubrouter.Handle("/sso", adminOnly(http.HandlerFunc(container.SSOHandler.SaveConfigHandler))).Methods(http.MethodPut)
	accountSubrouter.Handle("/sso", adminOnly(http.HandlerFunc(container.SSOHandler.DeleteConfigHandler))).Methods(http.MethodDelete)

	accountSubrouter.Handle("/users", adminOnly(http.HandlerFunc(container.UsersHandler.GetUsersHandler))).Methods(http.MethodGet)
	accountSubrouter.Handle("/users", adminOnly(http.HandlerFunc(container.UsersHandler.CreateUserHandler))).Methods(http.MethodPost)
	accountSubrouter.Handle("/users/{USER_ID}", adminOnly(http.HandlerFunc(container.UsersHandler.UpdateUserHandler))).Methods(http.MethodPatch)
	accountSubrouter.Handle("/users/{USER_ID}", adminOnly(http.HandlerFunc(container.UsersHandler.DeleteUserHandler))).Methods(http.MethodDelete)

	accountSubrouter.Handle("/audit/events", adminOnly(http.HandlerFunc(container.AuditHandler.ListAuditLogsHandler))).Methods(http.MethodGet)

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
