package infrastructure

import (
	"expo-open-ota/config"
	"expo-open-ota/ee/observe"
	"expo-open-ota/ee/rbac"
	dashutils "expo-open-ota/internal/dashboard"
	"expo-open-ota/internal/metrics"
	"expo-open-ota/internal/middleware"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gorilla/mux"
)

func HealthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func getDashboardPath() string {
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("Error getting executable path: %v", err)
	}
	exeDir := filepath.Dir(exePath)

	if strings.Contains(exePath, "/var/folders/") || strings.Contains(exePath, "Temp") {
		workingDir, _ := os.Getwd()
		return filepath.Join(workingDir, "apps", "dashboard", "dist")
	}
	return filepath.Join(exeDir, "apps", "dashboard", "dist")
}

func NewRouter(container *AppContainer) *mux.Router {
	r := mux.NewRouter()
	r.Use(middleware.LoggingMiddleware)
	// Every request carries its network context (client IP, user agent) so
	// audit events can be emitted from any layer below without the request.
	r.Use(middleware.RequestMetaMiddleware)

	if config.GetEnv("PROMETHEUS_ENABLED") == "true" {
		r.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			metrics.PrometheusHandler().ServeHTTP(w, r)
		}).Methods(http.MethodGet)
	}

	r.HandleFunc("/hc", HealthCheck).Methods(http.MethodGet)
	// Both routes answer 200 here, and that is correct: this router is only
	// swapped in once the bucket migrations are done, so the pod is by then both
	// alive and ready. The liveness/readiness split happens earlier, in
	// cmd/api/main.go's bootHandler, which registers /hc (200 throughout, so a
	// long migration never gets the pod killed) but deliberately leaves /ready
	// unregistered so it falls into that handler's catch-all 503 and keeps
	// traffic away until this router takes over.
	r.HandleFunc("/ready", HealthCheck).Methods(http.MethodGet)

	appSubrouter := r.PathPrefix("/{APP_ID}").Subrouter()
	appSubrouter.Use(middleware.AppResolverMiddleware(container.AppRepo))
	appSubrouter.HandleFunc("/requestUploadUrl/{BRANCH}", container.UploadHandler.RequestUploadUrlHandler).Methods(http.MethodPost)
	appSubrouter.HandleFunc("/uploadLocalFile", container.UploadHandler.RequestUploadLocalFileHandler).Methods(http.MethodPut)
	appSubrouter.HandleFunc("/markUpdateAsUploaded/{BRANCH}", container.UploadHandler.MarkUpdateAsUploadedHandler).Methods(http.MethodPost)
	appSubrouter.HandleFunc("/rollback/{BRANCH}", container.RollbackHandler.HandleRollback).Methods(http.MethodPost)
	appSubrouter.HandleFunc("/republish/{BRANCH}", container.RepublishHandler.HandleRepublish).Methods(http.MethodPost)

	// expo-observe ingestion (ee/observe), all under one /observe prefix. The
	// operator sets extra.eas.observe.endpointUrl to
	// https://<host>/observe/{APP_ID}; the SDK appends /{projectId}/v1/logs
	// with the app's REAL EAS project id (used by EAS builds, never equal to
	// our APP_ID), so the PROJECT_ID segment is deliberately ignored, exactly
	// as the SDK itself never validates it. Exact paths, no trailing-slash
	// variant: a gorilla 301 would turn the POST into a bodyless GET.
	observeSubrouter := r.PathPrefix("/observe/{APP_ID}").Subrouter()
	// The app check is memoized so telemetry (which fires on every
	// app-background of every device) does not issue an uncached primary-key
	// query per request.
	observeSubrouter.Use(observe.CachedAppResolverMiddleware(container.AppRepo))
	observeSubrouter.HandleFunc("/{PROJECT_ID}/v1/logs", container.ObserveIngestHandler.HandleLogs).Methods(http.MethodPost)
	observeSubrouter.HandleFunc("/{PROJECT_ID}/v1/metrics", container.ObserveIngestHandler.HandleMetrics).Methods(http.MethodPost)

	r.HandleFunc("/manifest", container.ExpoProtocolHandler.HandleManifest).Methods(http.MethodGet)
	r.HandleFunc("/assets", container.ExpoProtocolHandler.HandleAssets).Methods(http.MethodGet)

	corsSubrouter := r.PathPrefix("/auth").Subrouter()
	corsSubrouter.HandleFunc("/login", container.AuthHandler.LoginHandler).Methods(http.MethodPost)
	corsSubrouter.HandleFunc("/refreshToken", container.AuthHandler.RefreshTokenHandler).Methods(http.MethodPost)

	// Enterprise SSO (control-plane only). Pre-auth by nature: config feeds
	// the login page's SSO button, login/callback are the OIDC round-trip.
	// Registered unconditionally like the license routes; without a database,
	// a configuration or a valid license they answer accordingly.
	corsSubrouter.HandleFunc("/sso/config", container.SSOHandler.GetPublicConfigHandler).Methods(http.MethodGet)
	corsSubrouter.HandleFunc("/sso/login", container.SSOHandler.LoginRedirectHandler).Methods(http.MethodGet)
	corsSubrouter.HandleFunc("/sso/callback", container.SSOHandler.CallbackHandler).Methods(http.MethodGet)

	dashboardPath := getDashboardPath()

	if dashutils.IsDashboardEnabled() {
		r.PathPrefix("/dashboard").Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Get env.js
			if r.URL.Path == "/dashboard/env.js" {
				w.Header().Set("Content-Type", "application/javascript")
				baseURL := config.GetEnv("BASE_URL")
				if baseURL == "" {
					baseURL = "http://localhost:3000"
				}
				w.Write([]byte(fmt.Sprintf("window.env = { VITE_OTA_API_URL: '%s' };", baseURL)))
				return
			}
			if r.URL.Path == "/dashboard" {
				target := "/dashboard/"
				if r.URL.RawQuery != "" {
					target += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, target, http.StatusMovedPermanently)
				return
			}
			staticExtensions := []string{".css", ".js", ".svg", ".png", ".json", ".ico"}
			for _, ext := range staticExtensions {
				if len(r.URL.Path) > len(ext) && r.URL.Path[len(r.URL.Path)-len(ext):] == ext {
					filePath := filepath.Join(dashboardPath, r.URL.Path[len("/dashboard/"):])
					if !strings.HasPrefix(filePath, dashboardPath) {
						http.Error(w, "Forbidden", http.StatusForbidden)
						return
					}
					http.ServeFile(w, r, filePath)
					return
				}
			}
			filePath := filepath.Join(dashboardPath, "index.html")
			fmt.Println("Serving file", filePath)
			http.ServeFile(w, r, filePath)
		}))
	}

	authSubrouter := r.PathPrefix("/api").Subrouter()
	authSubrouter.Use(middleware.NewAuthMiddleware(container.DashboardAuthService, container.CliAuthService))
	authSubrouter.HandleFunc("/settings", container.SettingsHandler.GetSettingsHandler).Methods(http.MethodGet)

	// Two gates share the mutation routes. adminOnly guards the global
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

	// Current account
	authSubrouter.HandleFunc("/me", container.UsersHandler.GetMeHandler).Methods(http.MethodGet)
	authSubrouter.HandleFunc("/me/password", container.UsersHandler.ChangeMyPasswordHandler).Methods(http.MethodPut)

	// Enterprise license (control-plane only). Status is readable by every
	// signed-in account so the dashboard can reflect the edition; activating
	// or removing the key is admin-only.
	authSubrouter.HandleFunc("/license", container.LicenseHandler.GetLicenseHandler).Methods(http.MethodGet)
	authSubrouter.Handle("/license", adminOnly(http.HandlerFunc(container.LicenseHandler.ActivateLicenseHandler))).Methods(http.MethodPut)
	authSubrouter.Handle("/license", adminOnly(http.HandlerFunc(container.LicenseHandler.RemoveLicenseHandler))).Methods(http.MethodDelete)

	// Enterprise SSO configuration (control-plane only, admin only), managed
	// from the dashboard's License page.
	authSubrouter.Handle("/sso", adminOnly(http.HandlerFunc(container.SSOHandler.GetConfigHandler))).Methods(http.MethodGet)
	authSubrouter.Handle("/sso", adminOnly(http.HandlerFunc(container.SSOHandler.SaveConfigHandler))).Methods(http.MethodPut)
	authSubrouter.Handle("/sso", adminOnly(http.HandlerFunc(container.SSOHandler.DeleteConfigHandler))).Methods(http.MethodDelete)

	// Users management router (control-plane only, admin only)
	authSubrouter.Handle("/users", adminOnly(http.HandlerFunc(container.UsersHandler.GetUsersHandler))).Methods(http.MethodGet)
	authSubrouter.Handle("/users", adminOnly(http.HandlerFunc(container.UsersHandler.CreateUserHandler))).Methods(http.MethodPost)
	authSubrouter.Handle("/users/{USER_ID}", adminOnly(http.HandlerFunc(container.UsersHandler.UpdateUserHandler))).Methods(http.MethodPatch)
	authSubrouter.Handle("/users/{USER_ID}", adminOnly(http.HandlerFunc(container.UsersHandler.DeleteUserHandler))).Methods(http.MethodDelete)

	// Audit log (control-plane only, admin only). The log is append-only by
	// design, the retention purge being its single sanctioned exception;
	// reads are paginated and filterable.
	authSubrouter.Handle("/audit/events", adminOnly(http.HandlerFunc(container.AuditHandler.ListAuditLogsHandler))).Methods(http.MethodGet)

	// Enterprise user roles & per-app grants (control-plane only). Managing
	// who can do what is an administration surface, so every route is
	// admin-only; the license gate lives in the service (reads work without a
	// license so the dashboard can show dormant grants, writes refuse).
	// /me/permissions is the one exception: every signed-in account may ask
	// what it is allowed to do — display support, the middlewares re-check
	// every mutation anyway.
	authSubrouter.Handle("/roles", adminOnly(http.HandlerFunc(container.RBACHandler.ListRolesHandler))).Methods(http.MethodGet)
	authSubrouter.Handle("/roles", adminOnly(http.HandlerFunc(container.RBACHandler.CreateRoleHandler))).Methods(http.MethodPost)
	authSubrouter.Handle("/roles/{ROLE_ID}", adminOnly(http.HandlerFunc(container.RBACHandler.UpdateRoleHandler))).Methods(http.MethodPut)
	authSubrouter.Handle("/roles/{ROLE_ID}", adminOnly(http.HandlerFunc(container.RBACHandler.DeleteRoleHandler))).Methods(http.MethodDelete)
	authSubrouter.Handle("/users/{USER_ID}/grants", adminOnly(http.HandlerFunc(container.RBACHandler.GetUserGrantsHandler))).Methods(http.MethodGet)
	authSubrouter.Handle("/users/{USER_ID}/grants", adminOnly(http.HandlerFunc(container.RBACHandler.SetUserGrantsHandler))).Methods(http.MethodPut)
	authSubrouter.Handle("/users/grants/summary", adminOnly(http.HandlerFunc(container.RBACHandler.GetGrantSummaryHandler))).Methods(http.MethodGet)
	authSubrouter.HandleFunc("/me/permissions", container.RBACHandler.GetMyPermissionsHandler).Methods(http.MethodGet)

	// Apps management router. Creating an app is global administration and
	// stays admin-only; acting on an existing app is permission-gated.
	authSubrouter.Handle("/apps", adminOnly(http.HandlerFunc(container.AppHandler.CreateAppHandler))).Methods(http.MethodPost)
	authSubrouter.Handle("/apps/{APP_ID}", requirePermission(rbac.PermAppDelete)(http.HandlerFunc(container.AppHandler.DeleteAppHandler))).Methods(http.MethodDelete)
	authSubrouter.Handle("/apps/{APP_ID}", requirePermission(rbac.PermAppRename)(http.HandlerFunc(container.AppHandler.UpdateAppHandler))).Methods(http.MethodPatch)
	authSubrouter.HandleFunc("/apps", container.AppHandler.GetAppsHandler).Methods(http.MethodGet)
	// The signing certificate is key material — admins, or the explicit
	// certificate:read permission.
	authSubrouter.Handle("/apps/{APP_ID}/certificate", requirePermission(rbac.PermCertificateRead)(http.HandlerFunc(container.AppHandler.DownloadAppCertificateHandler))).Methods(http.MethodGet)

	// App-scoped dashboard routes: Auth first, then AppResolver validates the
	// id and short-circuits unknown apps with 404 before handlers run. Without
	// the resolver, an unknown id falls through to bucket lookups that return
	// empty lists — the client sees 200 with [] instead of a proper "no such
	// app" signal.
	appAuthSubrouter := authSubrouter.PathPrefix("/apps/{APP_ID}").Subrouter()
	appAuthSubrouter.StrictSlash(true)
	appAuthSubrouter.Use(middleware.AppResolverMiddleware(container.AppRepo))
	// After the resolver: enterprise visibility. While roles are enforced, a
	// member without a grant on this app gets the same 404 as an unknown id,
	// on reads and mutations alike. Validated CLI credentials pass through on
	// their context marker.
	appAuthSubrouter.Use(rbac.RequireAppVisible(container.RBACService))
	appAuthSubrouter.HandleFunc("/", container.AppHandler.GetAppHandler).Methods(http.MethodGet)
	appAuthSubrouter.Handle("/branches", requirePermission(rbac.PermBranchCreate)(http.HandlerFunc(container.BranchHandler.CreateBranchHandler))).Methods(http.MethodPost)
	appAuthSubrouter.Handle("/branches/{BRANCH}", requirePermission(rbac.PermBranchDelete)(http.HandlerFunc(container.BranchHandler.DeleteBranchHandler))).Methods(http.MethodDelete)
	appAuthSubrouter.HandleFunc("/branches", container.BranchHandler.GetBranchesHandler).Methods(http.MethodGet)
	appAuthSubrouter.Handle("/channels", requirePermission(rbac.PermChannelCreate)(http.HandlerFunc(container.ChannelHandler.CreateChannelHandler))).Methods(http.MethodPost)
	appAuthSubrouter.Handle("/channels/{CHANNEL}", requirePermission(rbac.PermChannelDelete)(http.HandlerFunc(container.ChannelHandler.DeleteChannelHandler))).Methods(http.MethodDelete)
	appAuthSubrouter.HandleFunc("/channels", container.ChannelHandler.GetChannelsHandler).Methods(http.MethodGet)
	// Progressive rollouts, control-plane only. One permission covers a channel
	// rollout's whole lifecycle, its per-update sibling has its own. There is
	// no read route: a channel carries its active rollout in the listing, so a
	// dedicated one answered a question already answered.
	appAuthSubrouter.Handle("/channels/{CHANNEL}/rollout", requirePermission(rbac.PermChannelRolloutManage)(http.HandlerFunc(container.RolloutHandler.StartChannelRolloutHandler))).Methods(http.MethodPost)
	appAuthSubrouter.Handle("/channels/{CHANNEL}/rollout", requirePermission(rbac.PermChannelRolloutManage)(http.HandlerFunc(container.RolloutHandler.UpdateChannelRolloutHandler))).Methods(http.MethodPatch)
	appAuthSubrouter.Handle("/channels/{CHANNEL}/rollout/end", requirePermission(rbac.PermChannelRolloutManage)(http.HandlerFunc(container.RolloutHandler.EndChannelRolloutHandler))).Methods(http.MethodPost)
	appAuthSubrouter.HandleFunc("/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/rollout", container.RolloutHandler.GetUpdateRolloutHandler).Methods(http.MethodGet)
	appAuthSubrouter.Handle("/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/rollout", requirePermission(rbac.PermUpdateRolloutManage)(http.HandlerFunc(container.RolloutHandler.SetUpdateRolloutPercentageHandler))).Methods(http.MethodPut)
	appAuthSubrouter.Handle("/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/rollout/revert", requirePermission(rbac.PermUpdateRolloutManage)(http.HandlerFunc(container.RolloutHandler.RevertUpdateRolloutHandler))).Methods(http.MethodPost)
	appAuthSubrouter.HandleFunc("/branch/{BRANCH}/runtimeVersions", container.BranchHandler.GetRuntimeVersionsHandler).Methods(http.MethodGet)
	appAuthSubrouter.HandleFunc("/updates", container.UpdateHandler.GetUpdateFeedHandler).Methods(http.MethodGet)
	appAuthSubrouter.HandleFunc("/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/updates", container.UpdateHandler.GetUpdatesHandler).Methods(http.MethodGet)
	appAuthSubrouter.HandleFunc("/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/updates/{UPDATE_ID}", container.UpdateHandler.GetUpdateDetailsHandler).Methods(http.MethodGet)
	appAuthSubrouter.Handle("/branch/{BRANCH_ID}/updateChannelBranchMapping", requirePermission(rbac.PermChannelEditBranch)(http.HandlerFunc(container.BranchHandler.UpdateChannelBranchMappingHandler))).Methods(http.MethodPost)
	// An API token is publishing power over the app — minting and revoking
	// need the apikeys:manage permission (or an admin). The list stays
	// readable: it only carries names and hints.
	appAuthSubrouter.Handle("/apiKeys", requirePermission(rbac.PermApiKeysManage)(http.HandlerFunc(container.ApiKeyHandler.CreateApiKeyHandler))).Methods(http.MethodPost)
	appAuthSubrouter.HandleFunc("/apiKeys", container.ApiKeyHandler.GetApiKeysHandler).Methods(http.MethodGet)
	appAuthSubrouter.Handle("/apiKeys/{API_KEY_ID}/revoke", requirePermission(rbac.PermApiKeysManage)(http.HandlerFunc(container.ApiKeyHandler.RevokeApiKeyHandler))).Methods(http.MethodDelete)
	// Enterprise: per-key access restrictions ride with the token permission
	// (they change what a token can do); toggling branch protection is its
	// own permission. Both stay license-gated in their service.
	appAuthSubrouter.HandleFunc("/apiKeys/restrictions", container.ApiKeyRestrictionHandler.GetApiKeyRestrictionsHandler).Methods(http.MethodGet)
	appAuthSubrouter.Handle("/apiKeys/{API_KEY_ID}/restrictions", requirePermission(rbac.PermApiKeysManage)(http.HandlerFunc(container.ApiKeyRestrictionHandler.SetApiKeyRestrictionsHandler))).Methods(http.MethodPut)
	appAuthSubrouter.Handle("/branches/{BRANCH}/protection", requirePermission(rbac.PermBranchProtect)(http.HandlerFunc(container.ApiKeyRestrictionHandler.SetBranchProtectionHandler))).Methods(http.MethodPut)
	// Device identity (ee/identity). Reads stay open to any app viewer; shaping
	// the allowlist needs the identity:manage permission (admins bypass it).
	appAuthSubrouter.HandleFunc("/identity/schema", container.IdentityHandler.GetSchemaHandler).Methods(http.MethodGet)
	appAuthSubrouter.Handle("/identity/schema/{KEY}", requirePermission(rbac.PermIdentityManage)(http.HandlerFunc(container.IdentityHandler.UpsertSchemaKeyHandler))).Methods(http.MethodPut)
	appAuthSubrouter.Handle("/identity/schema/{KEY}", requirePermission(rbac.PermIdentityManage)(http.HandlerFunc(container.IdentityHandler.DeleteSchemaKeyHandler))).Methods(http.MethodDelete)
	appAuthSubrouter.HandleFunc("/identity/values", container.IdentityHandler.SearchValuesHandler).Methods(http.MethodGet)
	appAuthSubrouter.HandleFunc("/identity/devices", container.IdentityHandler.ListDevicesHandler).Methods(http.MethodGet)
	appAuthSubrouter.HandleFunc("/identity/online", container.IdentityHandler.OnlineDevicesHandler).Methods(http.MethodGet)
	appAuthSubrouter.HandleFunc("/identity/devices/{EAS_CLIENT_ID}", container.IdentityHandler.GetDeviceHandler).Methods(http.MethodGet)
	// Instant-T adoption and launch health per update, straight from the
	// device registry (Postgres only, works without ClickHouse): feeds the
	// updates table's MAU column and the rollout card's health score.
	appAuthSubrouter.HandleFunc("/identity/update-health", container.IdentityHandler.UpdateHealthHandler).Methods(http.MethodGet)
	// Historical series is projected into ClickHouse. The endpoint stays
	// present without ClickHouse and reports available=false so the dashboard
	// can hide the graph while instant-T health keeps working.
	appAuthSubrouter.HandleFunc("/observe/update-health/history", container.ObserveHealthHistoryHandler.GetUpdateHealthHistoryHandler).Methods(http.MethodGet)
	// The Observe explorer, all read-only and all open to any viewer of the app,
	// like the other listings above: they aggregate telemetry, they never name a
	// user. Each one degrades to available=false inside the handler when
	// ClickHouse is absent rather than disappearing from the API. check-ins is
	// the map's incremental live feed, not a listing.
	appAuthSubrouter.HandleFunc("/observe/overview", container.ObserveExplorerHandler.GetOverviewHandler).Methods(http.MethodGet)
	appAuthSubrouter.HandleFunc("/observe/check-ins", container.ObserveExplorerHandler.GetCheckInsHandler).Methods(http.MethodGet)
	appAuthSubrouter.HandleFunc("/observe/events", container.ObserveExplorerHandler.GetEventsHandler).Methods(http.MethodGet)
	appAuthSubrouter.HandleFunc("/observe/logs", container.ObserveExplorerHandler.GetLogsHandler).Methods(http.MethodGet)
	appAuthSubrouter.HandleFunc("/observe/breakdown", container.ObserveExplorerHandler.GetBreakdownHandler).Methods(http.MethodGet)
	appAuthSubrouter.HandleFunc("/observe/conditions", container.ObserveExplorerHandler.GetConditionsHandler).Methods(http.MethodGet)
	return r
}
