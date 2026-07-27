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
// Observe explorer). Called by the dashboard, and by the eoas CLI on the two
// routes that say so.
//
// The app COLLECTION, /apps, is not here but in routes_account.go: it names no
// app, so it cannot sit behind the middlewares below, and it has to be
// registered before this file's subrouter opens anyway.
//
// AUTHENTICATION, in three layers.
//
//  1. NewAuthMiddleware, inherited from the /api subrouter built in NewRouter.
//     It resolves either credential, a dashboard session or an app-scoped CLI
//     publishing token, and nothing runs without one.
//
//  2. AppResolverMiddleware, then RequireAppVisible, both on the subrouter so
//     they cover every route without anyone having to remember. The resolver
//     validates the id and short-circuits unknown apps with 404 before
//     handlers run: without it, an unknown id falls through to bucket lookups
//     that return empty lists, and the client sees 200 with [] instead of a
//     proper "no such app" signal. RequireAppVisible is the enterprise half:
//     while roles are enforced, a member without a grant on this app gets the
//     same 404 as an unknown id. Validated CLI credentials pass through it on
//     their context marker, and are then judged by layer 3.
//
//  3. The route's own Access declaration, the third argument of every call
//     below. It answers three questions at once, and answering them is not
//     optional: the registration helper refuses a route that carries no
//     declaration, at boot.
//
//     AnyViewer()                any account that can see the app
//     AnyViewerOrToken()         the same, plus a CLI publishing token
//     NeedsPermission(perm, fb)  perm once roles are enforced, fb when not
//
// The fallback is per route because the right answer differs. Mutations use
// FallbackAdminOnly: refusing a member the ability to delete a branch without
// an enterprise license is the community edition working as intended. The
// telemetry reads use FallbackAnyMember: they were open to every member before
// these permissions existed, and taking that away from community deployments
// would be a regression rather than a hardening. Flipping one is a one-word
// change on its line.
func registerAppRoutes(
	apiSubrouter *mux.Router,
	container *AppContainer,
) {
	appAuthSubrouter := apiSubrouter.PathPrefix("/apps/{APP_ID}").Subrouter()
	// No StrictSlash. It used to be on, from when the app root was declared as
	// "/" and every caller sent the slashless form, so it existed to bridge
	// that gap with a 301. Registering the root as "" closed the gap, and what
	// StrictSlash was left doing was answering a trailing slash with a 301 on
	// EVERY method: Go's http.Client and curl -L both rewrite a 301 on a
	// non-GET method to GET, so DELETE /api/apps/{id}/ came back as a 200 read
	// and the caller saw success while nothing was deleted. No route here is
	// declared with a trailing slash, so turning it off costs nothing and a
	// trailing slash is now a plain 404.
	appAuthSubrouter.Use(middleware.AppResolverMiddleware(container.AppRepo))
	appAuthSubrouter.Use(rbac.RequireAppVisible(container.RBACService))

	app := appGroup{router: appAuthSubrouter, rbacService: container.RBACService}

	// The app itself, registered as "" so the path matches exactly what the
	// dashboard sends: /api/apps/{id}, no trailing slash, on all three.
	app.route(http.MethodGet, "", container.AppHandler.GetAppHandler,
		AnyViewer())
	app.route(http.MethodDelete, "", container.AppHandler.DeleteAppHandler,
		NeedsPermission(rbac.PermAppDelete, rbac.FallbackAdminOnly))
	app.route(http.MethodPatch, "", container.AppHandler.UpdateAppHandler,
		NeedsPermission(rbac.PermAppRename, rbac.FallbackAdminOnly))
	// The signing certificate is key material.
	app.route(http.MethodGet, "/certificate", container.AppHandler.DownloadAppCertificateHandler,
		NeedsPermission(rbac.PermCertificateRead, rbac.FallbackAdminOnly))

	// Branches and channels.
	app.route(http.MethodGet, "/branches", container.BranchHandler.GetBranchesHandler,
		AnyViewer())
	app.route(http.MethodPost, "/branches", container.BranchHandler.CreateBranchHandler,
		NeedsPermission(rbac.PermBranchCreate, rbac.FallbackAdminOnly))
	app.route(http.MethodDelete, "/branches/{BRANCH}", container.BranchHandler.DeleteBranchHandler,
		NeedsPermission(rbac.PermBranchDelete, rbac.FallbackAdminOnly))
	app.route(http.MethodGet, "/channels", container.ChannelHandler.GetChannelsHandler,
		AnyViewer())
	app.route(http.MethodPost, "/channels", container.ChannelHandler.CreateChannelHandler,
		NeedsPermission(rbac.PermChannelCreate, rbac.FallbackAdminOnly))
	app.route(http.MethodDelete, "/channels/{CHANNEL}", container.ChannelHandler.DeleteChannelHandler,
		NeedsPermission(rbac.PermChannelDelete, rbac.FallbackAdminOnly))
	app.route(http.MethodPost, "/branch/{BRANCH_ID}/updateChannelBranchMapping", container.BranchHandler.UpdateChannelBranchMappingHandler,
		NeedsPermission(rbac.PermChannelEditBranch, rbac.FallbackAdminOnly))
	app.route(http.MethodPut, "/branches/{BRANCH}/protection", container.ApiKeyRestrictionHandler.SetBranchProtectionHandler,
		NeedsPermission(rbac.PermBranchProtect, rbac.FallbackAdminOnly))

	// Progressive rollouts, control-plane only. One permission covers a channel
	// rollout's whole lifecycle, its per-update sibling has its own. There is
	// no channel rollout read route: a channel carries its active rollout in
	// the listing, so a dedicated one answered a question already answered.
	app.route(http.MethodPost, "/channels/{CHANNEL}/rollout", container.RolloutHandler.StartChannelRolloutHandler,
		NeedsPermission(rbac.PermChannelRolloutManage, rbac.FallbackAdminOnly))
	app.route(http.MethodPatch, "/channels/{CHANNEL}/rollout", container.RolloutHandler.UpdateChannelRolloutHandler,
		NeedsPermission(rbac.PermChannelRolloutManage, rbac.FallbackAdminOnly))
	app.route(http.MethodPost, "/channels/{CHANNEL}/rollout/end", container.RolloutHandler.EndChannelRolloutHandler,
		NeedsPermission(rbac.PermChannelRolloutManage, rbac.FallbackAdminOnly))
	app.route(http.MethodGet, "/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/rollout", container.RolloutHandler.GetUpdateRolloutHandler,
		AnyViewer())
	app.route(http.MethodPut, "/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/rollout", container.RolloutHandler.SetUpdateRolloutPercentageHandler,
		NeedsPermission(rbac.PermUpdateRolloutManage, rbac.FallbackAdminOnly))
	app.route(http.MethodPost, "/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/rollout/revert", container.RolloutHandler.RevertUpdateRolloutHandler,
		NeedsPermission(rbac.PermUpdateRolloutManage, rbac.FallbackAdminOnly))

	// Updates. The two the CLI reads are the only routes in this file a
	// publishing token may reach: eoas asks which runtime versions a branch
	// has, then which updates that pair already holds, before it publishes.
	app.route(http.MethodGet, "/branch/{BRANCH}/runtimeVersions", container.BranchHandler.GetRuntimeVersionsHandler,
		AnyViewerOrToken())
	app.route(http.MethodGet, "/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/updates", container.UpdateHandler.GetUpdatesHandler,
		AnyViewerOrToken())
	app.route(http.MethodGet, "/updates", container.UpdateHandler.GetUpdateFeedHandler,
		AnyViewer())
	app.route(http.MethodGet, "/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/updates/{UPDATE_ID}", container.UpdateHandler.GetUpdateDetailsHandler,
		AnyViewer())

	// API tokens. Minting and revoking one is publishing power over the app;
	// the list stays readable because it only carries names and hints.
	app.route(http.MethodGet, "/apiKeys", container.ApiKeyHandler.GetApiKeysHandler,
		AnyViewer())
	app.route(http.MethodPost, "/apiKeys", container.ApiKeyHandler.CreateApiKeyHandler,
		NeedsPermission(rbac.PermApiKeysManage, rbac.FallbackAdminOnly))
	app.route(http.MethodDelete, "/apiKeys/{API_KEY_ID}/revoke", container.ApiKeyHandler.RevokeApiKeyHandler,
		NeedsPermission(rbac.PermApiKeysManage, rbac.FallbackAdminOnly))
	// Enterprise: per-key access restrictions ride with the token permission,
	// since they change what a token can do. License-gated in their service.
	app.route(http.MethodGet, "/apiKeys/restrictions", container.ApiKeyRestrictionHandler.GetApiKeyRestrictionsHandler,
		AnyViewer())
	app.route(http.MethodPut, "/apiKeys/{API_KEY_ID}/restrictions", container.ApiKeyRestrictionHandler.SetApiKeyRestrictionsHandler,
		NeedsPermission(rbac.PermApiKeysManage, rbac.FallbackAdminOnly))

	// Device identity (ee/identity). Browsing the registry means reading
	// per-device data: the client id, whatever metadata the app attached, the
	// city and the coordinates. Seeing the app is no longer enough.
	app.route(http.MethodGet, "/identity/devices", container.IdentityHandler.ListDevicesHandler,
		NeedsPermission(rbac.PermIdentityRead, rbac.FallbackAnyMember))
	app.route(http.MethodGet, "/identity/devices/{EAS_CLIENT_ID}", container.IdentityHandler.GetDeviceHandler,
		NeedsPermission(rbac.PermIdentityRead, rbac.FallbackAnyMember))
	app.route(http.MethodGet, "/identity/values", container.IdentityHandler.SearchValuesHandler,
		NeedsPermission(rbac.PermIdentityRead, rbac.FallbackAnyMember))
	app.route(http.MethodGet, "/identity/online", container.IdentityHandler.OnlineDevicesHandler,
		NeedsPermission(rbac.PermIdentityRead, rbac.FallbackAnyMember))
	app.route(http.MethodGet, "/identity/schema", container.IdentityHandler.GetSchemaHandler,
		NeedsPermission(rbac.PermIdentityRead, rbac.FallbackAnyMember))
	app.route(http.MethodPut, "/identity/schema/{KEY}", container.IdentityHandler.UpsertSchemaKeyHandler,
		NeedsPermission(rbac.PermIdentityManage, rbac.FallbackAdminOnly))
	app.route(http.MethodDelete, "/identity/schema/{KEY}", container.IdentityHandler.DeleteSchemaKeyHandler,
		NeedsPermission(rbac.PermIdentityManage, rbac.FallbackAdminOnly))

	// Update health. The two open ones are aggregates per update naming no
	// device, and they feed the updates table's adoption column and the
	// rollout card's health score, which every member needs. Instant-T comes
	// straight from the device registry and works without ClickHouse; the
	// historical series is projected into it and reports available=false when
	// it is absent, so the dashboard hides the graph instead of losing the
	// route.
	app.route(http.MethodGet, "/identity/update-health", container.IdentityHandler.UpdateHealthHandler,
		AnyViewer())
	app.route(http.MethodGet, "/observe/update-health/history", container.ObserveHealthHistoryHandler.GetUpdateHealthHistoryHandler,
		AnyViewer())
	// Splitting that same window by a device dimension is a different question:
	// it reads device_model, os_version, country_code and their neighbours, the
	// very columns /observe/breakdown groups by just below. It is its own route
	// so that answer is a line here rather than a check buried in a handler.
	app.route(http.MethodGet, "/observe/update-health/segments", container.ObserveHealthHistoryHandler.GetUpdateHealthSegmentsHandler,
		NeedsPermission(rbac.PermObserveRead, rbac.FallbackAnyMember))

	// The Observe explorer. Read-only, and every one of them degrades to
	// available=false inside the handler when ClickHouse is absent rather than
	// disappearing from the API. They are permission-gated because of the log
	// feed above all: a record carries the client id, the session id and a body
	// the application wrote, so it holds whatever the app logged. The
	// aggregates ride with it rather than splitting the Observe section into
	// two halves a member could see one of.
	app.route(http.MethodGet, "/observe/overview", container.ObserveExplorerHandler.GetOverviewHandler,
		NeedsPermission(rbac.PermObserveRead, rbac.FallbackAnyMember))
	app.route(http.MethodGet, "/observe/check-ins", container.ObserveExplorerHandler.GetCheckInsHandler,
		NeedsPermission(rbac.PermObserveRead, rbac.FallbackAnyMember))
	app.route(http.MethodGet, "/observe/events", container.ObserveExplorerHandler.GetEventsHandler,
		NeedsPermission(rbac.PermObserveRead, rbac.FallbackAnyMember))
	app.route(http.MethodGet, "/observe/logs", container.ObserveExplorerHandler.GetLogsHandler,
		NeedsPermission(rbac.PermObserveRead, rbac.FallbackAnyMember))
	app.route(http.MethodGet, "/observe/breakdown", container.ObserveExplorerHandler.GetBreakdownHandler,
		NeedsPermission(rbac.PermObserveRead, rbac.FallbackAnyMember))
	app.route(http.MethodGet, "/observe/conditions", container.ObserveExplorerHandler.GetConditionsHandler,
		NeedsPermission(rbac.PermObserveRead, rbac.FallbackAnyMember))
}
