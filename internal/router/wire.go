package infrastructure

import (
	"context"
	"log"
	"time"
	"xprem/config"
	"xprem/ee/apikeyrestrictions"
	"xprem/ee/audit"
	"xprem/ee/branchprotection"
	"xprem/ee/geoip"
	"xprem/ee/identity"
	"xprem/ee/licensing"
	eemcptools "xprem/ee/mcptools"
	"xprem/ee/observe"
	"xprem/ee/rbac"
	"xprem/ee/sso"
	"xprem/ee/telemetry"
	"xprem/internal/bucket"
	"xprem/internal/cache"
	"xprem/internal/database"
	"xprem/internal/database/clickhouse"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/migrations"
	"xprem/internal/handlers"
	dashhandlers "xprem/internal/handlers/dashboard"
	"xprem/internal/mcp"
	"xprem/internal/mcptools"
	"xprem/internal/oauth"
	"xprem/internal/ratelimit"
	"xprem/internal/services"
	"xprem/internal/store"
)

type AppContainer struct {
	AuthHandler                 *dashhandlers.AuthHandler
	DashboardAuthService        *services.DashboardAuthService
	CliAuthService              *services.CliAuthService
	ApiKeyHandler               *dashhandlers.ApiKeyHandler
	ApiKeyAccessHandler         *apikeyrestrictions.ApiKeyAccessHandler
	BranchProtectionHandler     *branchprotection.Handler
	ApiKeyAccessService         *apikeyrestrictions.ApiKeyAccessService
	AppHandler                  *dashhandlers.AppHandler
	AppRepo                     services.AppRepository
	BranchHandler               *dashhandlers.BranchHandler
	BranchListHandler           *handlers.BranchListHandler
	ChannelHandler              *dashhandlers.ChannelHandler
	ExpoProtocolHandler         *handlers.ExpoProtocolHandler
	LicenseHandler              *licensing.LicenseHandler
	RBACHandler                 *rbac.RBACHandler
	RBACService                 *rbac.RBACService
	RollbackHandler             *handlers.RollbackHandler
	RolloutHandler              *dashhandlers.RolloutHandler
	MCPHandler                  *mcp.MCPHandler
	OAuthHandler                *oauth.OAuthHandler
	OAuthService                *oauth.OAuthService
	SettingsHandler             *dashhandlers.SettingsHandler
	SSOHandler                  *sso.SSOHandler
	UpdateHandler               *dashhandlers.UpdateHandler
	UploadHandler               *handlers.UploadHandler
	RepublishHandler            *handlers.RepublishHandler
	UsersHandler                *dashhandlers.UsersHandler
	AuditHandler                *audit.AuditHandler
	UserRepo                    services.UserRepository
	ObserveIngestHandler        *observe.IngestHandler
	ObserveHealthHistoryHandler *observe.HealthHistoryHandler
	ObserveExplorerHandler      *observe.ExplorerHandler
	IdentityHandler             *identity.IdentityHandler
}

// logLegacyAppIdFallback logs, once at boot, which app (if any) receives
// manifest and asset requests that carry no expo-app-id header.
func logLegacyAppIdFallback() {
	if appId := config.LegacyFallbackAppId(); appId != "" {
		log.Printf("🔁 [LEGACY] app id fallback ACTIVE for %s, v1 clients sending no expo-app-id header resolve to this app. Set SKIP_LEGACY_APP_ID_FALLBACK=true once every client ships the header.", appId)
		return
	}
	if config.GetEnv("EXPO_APP_ID") != "" {
		log.Println("🔒 [LEGACY] app id fallback DISABLED by SKIP_LEGACY_APP_ID_FALLBACK, manifest/asset requests without an expo-app-id header are rejected. Any v1 client that has not been rebuilt stops receiving updates.")
	}
}

func InitDependencies(ctx context.Context) (*AppContainer, func()) {
	var authRepo services.CliAuthRepository
	var appRepo services.AppRepository
	var branchRepo services.BranchRepository
	var channelRepo services.ChannelRepository
	var updateRepo services.UpdateRepository
	// nil in stateless mode: accounts only exist on the control plane.
	var userRepo services.UserRepository
	var refreshTokenRepo services.RefreshTokenRepository
	var oauthClientRepo oauth.ClientRepository
	var oauthCodeRepo oauth.CodeRepository
	var mcpHandler *mcp.MCPHandler
	var rolloutRepo services.RolloutRepository
	var licenseRepo licensing.LicenseRepository
	var ssoRepo sso.SSORepository
	var apiKeyAccessRepo apikeyrestrictions.ApiKeyAccessRepository
	var branchProtectionRepo branchprotection.Repository
	var rbacRepo rbac.RBACRepository
	var auditRepo audit.AuditRepository
	// nil in stateless mode and under DISABLE_DEVICE_TELEMETRY.
	var identityService *identity.Service
	var telemetrySink observe.TelemetrySink
	var branchResolver observe.BranchResolver
	var healthHistory *observe.HealthHistory
	var stateHistory *observe.StateHistory
	var explorer *observe.Explorer
	var checkInRecorder *observe.CheckInRecorder

	telemetryEnabled := !config.IsServerTelemetryDisabled() && !config.IsTestMode()
	var instanceId string
	var instanceIdErr error

	cleanup := func() {}
	// Releases in reverse acquisition order.
	addCleanup := func(release func()) {
		previous := cleanup
		cleanup = func() {
			release()
			previous()
		}
	}
	dbUrl := config.GetDBURL()

	resolvedBucket := bucket.GetBucket()

	if dbUrl != "" {
		if !database.IsValidDBURL(dbUrl) {
			log.Fatalf("Invalid database URL: %s", dbUrl)
		}
		err := config.ValidateMasterKey()
		if err != nil {
			log.Fatalf("Invalid master key configuration: %v", err)
		}
		log.Println("⚙️  [CONTROL] Initializing Control Plane (DB Mode)..")
		dbConfig := database.LoadDBConfigFromEnv()
		dbEngine, err := database.NewPostgresEngine(ctx, dbConfig)
		if err != nil {
			log.Fatalf("Database initialization failed: %v", err)
		}
		cleanup = func() { dbEngine.Close() }
		migrations.SetEngine(dbEngine)
		postgres.RunDBMigrations(dbUrl)

		authRepo = store.NewPostgresAuthStore(dbEngine)
		appRepo = store.NewPostgresAppStore(dbEngine)
		userRepo = store.NewPostgresUserStore(dbEngine)
		refreshTokenRepo = store.NewPostgresRefreshTokenStore(dbEngine)
		oauthClientRepo = store.NewPostgresOAuthClientStore(dbEngine)
		oauthCodeRepo = store.NewPostgresOAuthCodeStore(dbEngine)
		licenseRepo = licensing.NewPostgresLicenseStore(dbEngine)
		ssoRepo = sso.NewPostgresSSOStore(dbEngine)
		apiKeyAccessRepo = apikeyrestrictions.NewPostgresApiKeyAccessStore(dbEngine)
		branchProtectionRepo = branchprotection.NewPostgresStore(dbEngine)
		rbacRepo = rbac.NewPostgresRBACStore(dbEngine)
		auditRepo = audit.NewPostgresAuditStore(dbEngine)
		branchRepo = store.NewPostgresBranchStore(dbEngine)
		channelRepo = store.NewPostgresChannelStore(dbEngine)
		pgUpdateStore := store.NewPostgresUpdateStore(dbEngine)
		updateRepo = pgUpdateStore
		rolloutRepo = store.NewPostgresRolloutStore(dbEngine)

		// Resolved even when telemetry is off: licensing needs the instance id.
		seedInstanceId, _ := resolvedBucket.GetInstanceID()
		instanceId, instanceIdErr = store.NewPostgresServerInstanceStore(dbEngine).GetOrCreateInstanceID(ctx, seedInstanceId)
		if instanceIdErr != nil {
			log.Printf("⚠️  [INSTANCE] Could not resolve the server instance id, license activation and heartbeats are unavailable this run: %v", instanceIdErr)
		}

		if config.IsDeviceTelemetryDisabled() {
			log.Println("🔕 [TELEMETRY] DISABLE_DEVICE_TELEMETRY is set; nothing is recorded about a device: manifest check-ins, identity ops and telemetry batches are all dropped, and no ClickHouse connection is opened. The Observe and Identity dashboards report the feature as unavailable, and CLICKHOUSE_URL is ignored.")
			addCleanup(observe.StartHealthOutboxDiscarder(ctx, dbEngine))
		} else {
			stateHistory = observe.NewStateHistory(dbEngine)
			identityService = identity.NewService(identity.NewPostgresIdentityStore(dbEngine))
			checkInRecorder = observe.NewCheckInRecorder(identityService, cache.GetCache())
			var observeClickHouse *clickhouse.Engine

			if chUrl := config.GetClickHouseURL(); chUrl != "" {
				chEngine, err := clickhouse.NewClickHouseEngine(ctx, chUrl)
				if err != nil {
					log.Fatalf("🚨 [CLICKHOUSE] %v", err)
				}
				addCleanup(chEngine.Close)
				clickhouse.RunDBMigrations(chUrl, dbUrl)
				telemetrySink = observe.NewClickHouseTelemetrySink(chEngine)
				branchResolver = observe.NewBranchResolver(cache.GetCache(), pgUpdateStore.GetUpdateOriginByUUID)
				healthHistory = observe.NewHealthHistory(dbEngine, chEngine)
				observeClickHouse = chEngine
				addCleanup(healthHistory.Start(ctx))
			} else {
				log.Println("⚙️  [OBSERVE] CLICKHOUSE_URL is not set; telemetry ingestion (metrics/logs) stays disabled")
				addCleanup(observe.StartHealthOutboxDiscarder(ctx, dbEngine))
			}
			explorer = observe.NewExplorer(dbEngine, observeClickHouse)
		}
	} else {
		log.Println("⚙️  [STATELESS] Initializing Stateless Mode (Flat-Env Mode)...")
		if err := config.LoadAppsFromFlatEnv(); err != nil {
			log.Fatalf("Invalid apps config: %v\nSee https://mercure-technologies.gitbook.io/xprem/stateless-mode/getting-started for the stateless (flat-env) config format.", err)
		}
		authRepo = store.NewBucketAuthStore(resolvedBucket)
		appRepo = store.NewBucketAppStore(resolvedBucket)
		branchRepo = store.NewBucketBranchStore(resolvedBucket)
		channelRepo = store.NewBucketChannelStore(resolvedBucket)
		updateRepo = store.NewBucketUpdateStore(resolvedBucket)
		if telemetryEnabled {
			instanceId, instanceIdErr = store.NewBucketServerInstanceStore(resolvedBucket, cache.GetCache()).GetOrCreateInstanceID(ctx)
			if instanceIdErr != nil {
				log.Printf("⚠️  [TELEMETRY] Could not resolve the server instance id, heartbeats stay off for this run: %v", instanceIdErr)
			}
		}
	}

	if telemetryEnabled && instanceIdErr == nil {
		log.Println("📡 [TELEMETRY] Hourly usage ping enabled (instance id, base URL, version, configuration shape); set DISABLE_TELEMETRY=true to opt out")
		telemetry.NewTelemetryService(userRepo, appRepo, instanceId).Start(ctx)
	}

	// The router starts the geo resolver in every mode, so its cleanup does
	// not belong to the DB branch.
	addCleanup(geoip.CloseResolver)

	logLegacyAppIdFallback()

	auditService := audit.NewAuditService(auditRepo)
	if err := auditService.StartArchiveFromEnv(ctx); err != nil {
		log.Fatalf("🚨 [AUDIT] %v", err)
	}
	auditService.StartRetentionPurgeFromEnv(ctx)

	licenseClient := licensing.NewClient(config.GetEnv("LICENSE_API_URL"))
	licenseService := licensing.NewLicenseService(licenseRepo, licenseClient, instanceId, config.GetEnv("BASE_URL"))
	// Wired before the loops start: they emit audit events from goroutines.
	licenseService.SetOnAuditEvent(auditService.Record)
	if err := licenseService.ActivateFromStore(ctx); err != nil {
		log.Printf("⚠️  [LICENSE] Could not load the enterprise license from the database: %v", err)
	}
	licenseService.StartSync(ctx, 30*time.Second)
	if !config.IsTestMode() {
		licenseService.StartValidationLoop(ctx)
	}

	apiKeyAccessService := apikeyrestrictions.NewApiKeyAccessService(apiKeyAccessRepo)
	branchProtectionService := branchprotection.NewService(branchProtectionRepo)
	apiKeyAccessService.SetOnAuditEvent(auditService.Record)
	branchProtectionService.SetOnAuditEvent(auditService.Record)
	rbacService := rbac.NewRBACService(rbacRepo, userRepo)
	rbacService.SetOnAuditEvent(auditService.Record)
	visibleApps := rbacService.VisibleAppsForPrincipal
	dashboardAuthService := services.NewDashboardAuthService(userRepo, refreshTokenRepo)
	cliAuthService := services.NewCliAuthService(authRepo)
	cliAuthService.SetAuditActive(auditService.Enabled)
	cliAuthService.SetOnAuditEvent(auditService.Record)
	userService := services.NewUserService(userRepo)
	ssoService := sso.NewSSOService(ssoRepo, userRepo, dashboardAuthService)
	dashboardAuthService.SetSSOEnforced(ssoService.Enabled)
	userService.SetSSOEnforced(ssoService.Enabled)
	dashboardAuthService.SetOnAuditEvent(auditService.Record)
	ssoService.SetOnAuditEvent(auditService.Record)
	userService.SetOnAuditEvent(auditService.Record)
	appService := services.NewAppService(appRepo)
	appService.SetOnAuditEvent(auditService.Record)
	branchService := services.NewBranchService(branchRepo, channelRepo, updateRepo, rolloutRepo, resolvedBucket)
	branchService.SetOnAuditEvent(auditService.Record)
	channelService := services.NewChannelService(branchRepo, channelRepo)
	channelService.SetOnAuditEvent(auditService.Record)
	updateService := services.NewUpdateService(updateRepo, resolvedBucket)
	expoProtocolService := services.NewExpoProtocolService(appRepo, channelRepo, updateRepo, updateService, services.DefaultBranchRules())
	deploymentService := services.NewDeploymentService(branchService, updateService, updateRepo, resolvedBucket)
	deploymentService.SetOnAuditEvent(auditService.Record)
	rolloutService := services.NewRolloutService(rolloutRepo, channelRepo, updateRepo, deploymentService)
	rolloutService.SetOnAuditEvent(auditService.Record)

	// Shared across handlers; with Redis configured, also across replicas.
	rateLimiter := ratelimit.New(cache.GetCache())

	var oauthService *oauth.OAuthService
	var oauthHandler *oauth.OAuthHandler
	if oauthClientRepo != nil {
		oauthService = oauth.NewOAuthService(oauthClientRepo, oauthCodeRepo, refreshTokenRepo, userRepo)
		oauthHandler = oauth.NewOAuthHandler(oauthService, rateLimiter)

		rbac.MustValidateMCPTools(mcptools.DeclaredPermissions(), eemcptools.DeclaredPermissions())
		mcpHandler = mcp.NewMCPHandler(mcp.NewMCPService(
			mcptools.Configurator(mcptools.Deps{
				Apps:                appRepo,
				Branches:            branchService,
				Channels:            channelService,
				UpdateFeed:          updateService,
				UpdateRollouts:      rolloutService,
				Certificates:        appService,
				BranchWriter:        branchService,
				ChannelWriter:       channelService,
				Deployments:         deploymentService,
				SSOEnabled:          ssoService.Enabled,
				VisibleApps:         rbacService.VisibleAppsForPrincipal,
				CanUseSomewhere:     rbacService.MCPCanUseSomewhere,
				Authorize:           rbacService.MCPAuthorizeTool,
				DescribePermissions: rbacService.MCPDescribePermissions,
			}),
			eemcptools.Configurator(eemcptools.Deps{
				CanUseSomewhere: rbacService.MCPCanUseSomewhere,
				Authorize:       rbacService.MCPAuthorizeTool,
				Audit:           auditService,
				Apps:            appRepo,
				VisibleApps:     rbacService.VisibleAppsForPrincipal,
				Identity:        identityService,
				HealthHistory:   healthHistory,
				StateHistory:    stateHistory,
				UpdateFeed:      updateService,
				Explorer:        explorer,
			}),
		))
	}

	container := &AppContainer{
		AuthHandler:                 dashhandlers.NewAuthHandler(dashboardAuthService, rateLimiter),
		DashboardAuthService:        dashboardAuthService,
		CliAuthService:              cliAuthService,
		ApiKeyHandler:               dashhandlers.NewApiKeyHandler(cliAuthService),
		ApiKeyAccessHandler:         apikeyrestrictions.NewApiKeyAccessHandler(apiKeyAccessService),
		ApiKeyAccessService:         apiKeyAccessService,
		BranchProtectionHandler:     branchprotection.NewHandler(branchProtectionService),
		AppHandler:                  dashhandlers.NewAppHandler(appService, visibleApps),
		AppRepo:                     appRepo,
		BranchHandler:               dashhandlers.NewBranchHandler(branchService),
		BranchListHandler:           handlers.NewBranchListHandler(channelService),
		ChannelHandler:              dashhandlers.NewChannelHandler(channelService),
		ExpoProtocolHandler:         handlers.NewExpoProtocolHandler(expoProtocolService),
		LicenseHandler:              licensing.NewLicenseHandler(licenseService),
		AuditHandler:                audit.NewAuditHandler(auditService),
		RBACHandler:                 rbac.NewRBACHandler(rbacService),
		RBACService:                 rbacService,
		RepublishHandler:            handlers.NewRepublishHandler(deploymentService),
		RollbackHandler:             handlers.NewRollbackHandler(deploymentService),
		RolloutHandler:              dashhandlers.NewRolloutHandler(rolloutService, updateService),
		SettingsHandler:             dashhandlers.NewSettingsHandler(appService, ssoService.Enabled, visibleApps),
		SSOHandler:                  sso.NewSSOHandler(ssoService, rateLimiter),
		UpdateHandler:               dashhandlers.NewUpdateHandler(updateService, deploymentService),
		UploadHandler:               handlers.NewUploadHandler(deploymentService),
		UsersHandler:                dashhandlers.NewUsersHandler(userService, dashboardAuthService, rateLimiter),
		UserRepo:                    userRepo,
		ObserveIngestHandler:        observe.NewIngestHandler(identityService, telemetrySink, branchResolver, checkInRecorder),
		ObserveHealthHistoryHandler: observe.NewHealthHistoryHandler(healthHistory, stateHistory),
		ObserveExplorerHandler:      observe.NewExplorerHandler(explorer, identityService),
		IdentityHandler:             identity.NewIdentityHandler(identityService),
		MCPHandler:                  mcpHandler,
		OAuthHandler:                oauthHandler,
		OAuthService:                oauthService,
	}

	if checkInRecorder != nil {
		container.ExpoProtocolHandler.SetOnDeviceCheckIn(checkInRecorder.Record)
	}

	return container, cleanup
}
