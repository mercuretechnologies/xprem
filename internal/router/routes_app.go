package infrastructure

import (
	"expo-open-ota/ee/rbac"
	"expo-open-ota/internal/middleware"
	"net/http"

	"github.com/gorilla/mux"
)

// Everything scoped to one app under /api/apps/{APP_ID}: the app itself, its
// certificate, its branches and channels, its rollouts, its updates, its API
// keys, and the Enterprise reads on top of those (device identity and the
// Observe explorer). Called by the dashboard.
//
// The app COLLECTION, /apps, is not here but in routes_account.go: it names no
// app, so it cannot sit behind the middlewares below, and it has to be
// registered before this file's subrouter opens anyway.
//
// AUTHENTICATION, in three layers, and the order between the first two is the
// point of the whole group.
//
//  1. NewAuthMiddleware, inherited from the /api subrouter built in NewRouter.
//     It resolves a principal from either credential, and nothing runs without
//     one.
//
//  2. AppResolverMiddleware, then RequireAppVisible, both on the subrouter so
//     they cover every route below without anyone having to remember. The
//     resolver validates the id and short-circuits unknown apps with 404
//     before handlers run: without it, an unknown id falls through to bucket
//     lookups that return empty lists, and the client sees 200 with [] instead
//     of a proper "no such app" signal. RequireAppVisible is the enterprise
//     half: while roles are enforced, a member without a grant on this app
//     gets the same 404 as an unknown id, on reads and mutations alike.
//     Validated CLI credentials pass through on their context marker.
//
//  3. requirePermission, per route, on the mutations. Admins always pass;
//     members need the permission on this route's app through their enterprise
//     grants (ee/rbac). Without a control plane or a valid license it degrades
//     to exactly adminOnly's behavior, which keeps members read-only. It wraps
//     individual routes rather than the subrouter because the reads next to
//     them are deliberately open to any viewer of the app.
//
// So a route registered here WITHOUT requirePermission is readable by anyone
// who can see the app, and that is the rule to read the file by.
func registerAppRoutes(
	apiSubrouter *mux.Router,
	container *AppContainer,
	requirePermission func(rbac.Permission) mux.MiddlewareFunc,
) {
	appAuthSubrouter := apiSubrouter.PathPrefix("/apps/{APP_ID}").Subrouter()
	appAuthSubrouter.StrictSlash(true)
	appAuthSubrouter.Use(middleware.AppResolverMiddleware(container.AppRepo))
	appAuthSubrouter.Use(rbac.RequireAppVisible(container.RBACService))

	// The app itself. Registered as "" and not "/", which is what makes the
	// path match exactly as the dashboard sends it: with StrictSlash on this
	// subrouter, a route declared "/" answers a slashless call with a 301 to
	// the slashed form, and every one of these three is called without one.
	// The redirect would still work, browsers follow it and keep the method,
	// it is simply a round trip nobody needs. The slashed form keeps working
	// either way, StrictSlash redirects in both directions.
	appAuthSubrouter.HandleFunc("", container.AppHandler.GetAppHandler).Methods(http.MethodGet)
	appAuthSubrouter.Handle("", requirePermission(rbac.PermAppDelete)(http.HandlerFunc(container.AppHandler.DeleteAppHandler))).Methods(http.MethodDelete)
	appAuthSubrouter.Handle("", requirePermission(rbac.PermAppRename)(http.HandlerFunc(container.AppHandler.UpdateAppHandler))).Methods(http.MethodPatch)
	// The signing certificate is key material: admins, or the explicit
	// certificate:read permission.
	appAuthSubrouter.Handle("/certificate", requirePermission(rbac.PermCertificateRead)(http.HandlerFunc(container.AppHandler.DownloadAppCertificateHandler))).Methods(http.MethodGet)
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
}
