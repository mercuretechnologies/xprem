package infrastructure

import (
	"net/http"
	"xprem/ee/apikeyrestrictions"
	"xprem/ee/rbac"
	"xprem/internal/middleware"

	"github.com/gorilla/mux"
)

// registerAppRoutes registers everything scoped to one app under
// /api/apps/{APP_ID}: the app itself, its branches and channels, rollouts,
// updates, API keys, and the enterprise reads on top (device identity and
// the Observe explorer).
func registerAppRoutes(
	apiSubrouter *mux.Router,
	container *AppContainer,
) {
	appAuthSubrouter := apiSubrouter.PathPrefix("/apps/{APP_ID}").Subrouter()
	appAuthSubrouter.Use(middleware.AppResolverMiddleware(container.AppRepo))
	appAuthSubrouter.Use(rbac.RequireAppVisible(container.RBACService))

	app := appGroup{
		router:       appAuthSubrouter,
		rbacService:  container.RBACService,
		apiKeyAccess: container.ApiKeyAccessService,
	}

	app.route(http.MethodGet, "", container.AppHandler.GetAppHandler,
		AnyViewer())
	app.route(http.MethodDelete, "", container.AppHandler.DeleteAppHandler,
		NeedsPermission(rbac.PermAppDelete, rbac.FallbackAdminOnly))
	app.route(http.MethodPatch, "", container.AppHandler.UpdateAppHandler,
		NeedsPermission(rbac.PermAppRename, rbac.FallbackAdminOnly))
	app.route(http.MethodGet, "/certificate", container.AppHandler.DownloadAppCertificateHandler,
		NeedsPermission(rbac.PermCertificateRead, rbac.FallbackAdminOnly))

	app.route(http.MethodGet, "/branches", container.BranchHandler.GetBranchesHandler,
		AnyViewer())
	app.route(http.MethodPost, "/branches", container.BranchHandler.CreateBranchHandler,
		NeedsPermission(rbac.PermBranchCreate, rbac.FallbackAdminOnly))
	app.route(http.MethodDelete, "/branches/{BRANCH}", container.BranchHandler.DeleteBranchHandler,
		NeedsPermission(rbac.PermBranchDelete, rbac.FallbackAdminOnly))
	app.route(http.MethodPut, "/branches/{BRANCH}/protection", container.BranchProtectionHandler.SetBranchProtectionHandler,
		NeedsPermission(rbac.PermBranchProtect, rbac.FallbackAdminOnly))
	app.route(http.MethodGet, "/channels", container.ChannelHandler.GetChannelsHandler,
		AnyViewer())
	app.route(http.MethodPost, "/channels", container.ChannelHandler.CreateChannelHandler,
		NeedsPermission(rbac.PermChannelCreate, rbac.FallbackAdminOnly))
	app.route(http.MethodDelete, "/channels/{CHANNEL}", container.ChannelHandler.DeleteChannelHandler,
		NeedsPermission(rbac.PermChannelDelete, rbac.FallbackAdminOnly))
	app.route(http.MethodPut, "/channels/{CHANNEL}/branch-surfing", container.ChannelHandler.SetBranchSurfingHandler,
		NeedsPermission(rbac.PermChannelBranchSurfing, rbac.FallbackAdminOnly))
	app.route(http.MethodPost, "/branch/{BRANCH_ID}/updateChannelBranchMapping", container.BranchHandler.UpdateChannelBranchMappingHandler,
		NeedsPermission(rbac.PermChannelEditBranch, rbac.FallbackAdminOnly))

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

	// Updates. The three the CLI reads are the only routes in this file a
	// publishing token may reach: eoas asks which runtime versions a branch
	// has, then which updates or publish groups that pair already holds.
	app.route(http.MethodGet, "/branch/{BRANCH}/runtimeVersions", container.BranchHandler.GetRuntimeVersionsHandler,
		AnyViewerOrToken(apikeyrestrictions.ActionRead))
	app.route(http.MethodGet, "/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/updates", container.UpdateHandler.GetUpdatesHandler,
		AnyViewerOrToken(apikeyrestrictions.ActionRead))
	app.route(http.MethodGet, "/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/publish-groups", container.UpdateHandler.GetPublishGroupsHandler,
		AnyViewerOrToken(apikeyrestrictions.ActionRead))
	app.route(http.MethodGet, "/updates", container.UpdateHandler.GetUpdateFeedHandler,
		AnyViewer())
	app.route(http.MethodGet, "/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/updates/{UPDATE_ID}", container.UpdateHandler.GetUpdateDetailsHandler,
		AnyViewer())
	app.route(http.MethodPost, "/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/rollback", container.UpdateHandler.CreateRollbackHandler,
		NeedsPermission(rbac.PermUpdatePublish, rbac.FallbackAdminOnly))
	app.route(http.MethodPost, "/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/republish", container.UpdateHandler.RepublishUpdateHandler,
		NeedsPermission(rbac.PermUpdatePublish, rbac.FallbackAdminOnly))

	app.route(http.MethodGet, "/identifiers", container.AppIdentifiersHandler.GetAppIdentifiersHandler,
		AnyViewer())
	app.route(http.MethodPost, "/identifiers", container.AppIdentifiersHandler.CreateAppIdentifierHandler,
		NeedsPermission(rbac.PermCredentialsManage, rbac.FallbackAdminOnly))
	app.route(http.MethodDelete, "/identifiers/{IDENTIFIER_ID}", container.AppIdentifiersHandler.DeleteAppIdentifierHandler,
		NeedsPermission(rbac.PermCredentialsManage, rbac.FallbackAdminOnly))
	app.route(http.MethodPut, "/identifiers/{IDENTIFIER_ID}/build-number", container.AppIdentifiersHandler.SetBuildNumberHandler,
		NeedsPermission(rbac.PermCredentialsManage, rbac.FallbackAdminOnly))
	app.route(http.MethodGet, "/identifiers/{IDENTIFIER_ID}/credentials/android", container.CredentialsHandler.GetAndroidCredentialsHandler,
		AnyViewer())
	app.route(http.MethodPut, "/identifiers/{IDENTIFIER_ID}/credentials/android", container.CredentialsHandler.PutAndroidCredentialsHandler,
		NeedsPermission(rbac.PermCredentialsManage, rbac.FallbackAdminOnly))
	app.route(http.MethodDelete, "/identifiers/{IDENTIFIER_ID}/credentials/android", container.CredentialsHandler.DeleteAndroidCredentialsHandler,
		NeedsPermission(rbac.PermCredentialsManage, rbac.FallbackAdminOnly))

	app.route(http.MethodGet, "/environments", container.EnvironmentsHandler.ListEnvironmentsHandler,
		AnyViewer())
	app.route(http.MethodPost, "/environments", container.EnvironmentsHandler.CreateEnvironmentHandler,
		NeedsPermission(rbac.PermEnvManage, rbac.FallbackAdminOnly))
	app.route(http.MethodDelete, "/environments/{ENVIRONMENT}", container.EnvironmentsHandler.DeleteEnvironmentHandler,
		NeedsPermission(rbac.PermEnvManage, rbac.FallbackAdminOnly))
	app.route(http.MethodPut, "/environments/{ENVIRONMENT}/vars/{KEY}", container.EnvironmentsHandler.SetEnvVarHandler,
		NeedsPermission(rbac.PermEnvManage, rbac.FallbackAdminOnly))
	app.route(http.MethodGet, "/environments/{ENVIRONMENT}/vars/{KEY}/value", container.EnvironmentsHandler.RevealEnvVarHandler,
		NeedsPermission(rbac.PermEnvRead, rbac.FallbackAnyMember))
	app.route(http.MethodDelete, "/environments/{ENVIRONMENT}/vars/{KEY}", container.EnvironmentsHandler.DeleteEnvVarHandler,
		NeedsPermission(rbac.PermEnvManage, rbac.FallbackAdminOnly))
	app.route(http.MethodPut, "/channels/{CHANNEL}/environment", container.EnvironmentsHandler.SetChannelEnvironmentHandler,
		NeedsPermission(rbac.PermEnvManage, rbac.FallbackAdminOnly))

	app.route(http.MethodGet, "/apiKeys", container.ApiKeyHandler.GetApiKeysHandler,
		AnyViewer())
	app.route(http.MethodPost, "/apiKeys", container.ApiKeyHandler.CreateApiKeyHandler,
		NeedsPermission(rbac.PermApiKeysManage, rbac.FallbackAdminOnly))
	app.route(http.MethodDelete, "/apiKeys/{API_KEY_ID}/revoke", container.ApiKeyHandler.RevokeApiKeyHandler,
		NeedsPermission(rbac.PermApiKeysManage, rbac.FallbackAdminOnly))
	app.route(http.MethodGet, "/apiKeys/access", container.ApiKeyAccessHandler.GetApiKeyAccessHandler,
		AnyViewer())
	app.route(http.MethodPut, "/apiKeys/{API_KEY_ID}/access", container.ApiKeyAccessHandler.SetApiKeyAccessHandler,
		NeedsPermission(rbac.PermApiKeysManage, rbac.FallbackAdminOnly))

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

	app.route(http.MethodGet, "/identity/update-health", container.IdentityHandler.UpdateHealthHandler,
		AnyViewer())
	app.route(http.MethodGet, "/observe/update-health/history", container.ObserveHealthHistoryHandler.GetUpdateHealthHistoryHandler,
		AnyViewer())
	app.route(http.MethodGet, "/observe/update-health/segments", container.ObserveHealthHistoryHandler.GetUpdateHealthSegmentsHandler,
		NeedsPermission(rbac.PermObserveRead, rbac.FallbackAnyMember))

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
