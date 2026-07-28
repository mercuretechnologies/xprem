package infrastructure

import (
	"context"
	"expo-open-ota/config"
	"expo-open-ota/ee/apikeyrestrictions"
	"expo-open-ota/ee/audit"
	"expo-open-ota/ee/identity"
	"expo-open-ota/ee/licensing"
	"expo-open-ota/ee/observe"
	"expo-open-ota/ee/rbac"
	"expo-open-ota/ee/sso"
	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/clickhouse"
	"expo-open-ota/internal/database/postgres"
	"expo-open-ota/internal/database/postgres/migrations"
	"expo-open-ota/internal/handlers"
	dashhandlers "expo-open-ota/internal/handlers/dashboard"
	"expo-open-ota/internal/ratelimit"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"
	"log"
	"time"
)

type AppContainer struct {
	AuthHandler                 *dashhandlers.AuthHandler
	DashboardAuthService        *services.DashboardAuthService
	CliAuthService              *services.CliAuthService
	ApiKeyHandler               *dashhandlers.ApiKeyHandler
	ApiKeyRestrictionHandler    *apikeyrestrictions.ApiKeyRestrictionHandler
	AppHandler                  *dashhandlers.AppHandler
	AppRepo                     services.AppRepository
	BranchHandler               *dashhandlers.BranchHandler
	ChannelHandler              *dashhandlers.ChannelHandler
	ExpoProtocolHandler         *handlers.ExpoProtocolHandler
	LicenseHandler              *licensing.LicenseHandler
	RBACHandler                 *rbac.RBACHandler
	RBACService                 *rbac.RBACService
	RollbackHandler             *handlers.RollbackHandler
	RolloutHandler              *dashhandlers.RolloutHandler
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

// logLegacyAppIdFallback states, once at boot, which app receives manifest and
// asset requests that carry no expo-app-id header. Whether v1 clients still get
// updates hinges on this and is otherwise invisible until someone notices an
// install has silently stopped updating, so it is worth a line in the log.
func logLegacyAppIdFallback() {
	if appId := config.LegacyFallbackAppId(); appId != "" {
		log.Printf("🔁 [LEGACY] app id fallback ACTIVE for %s — v1 clients sending no expo-app-id header resolve to this app. Set SKIP_LEGACY_APP_ID_FALLBACK=true once every client ships the header.", appId)
		return
	}
	if config.GetEnv("EXPO_APP_ID") != "" {
		log.Println("🔒 [LEGACY] app id fallback DISABLED by SKIP_LEGACY_APP_ID_FALLBACK — manifest/asset requests without an expo-app-id header are rejected. Any v1 client that has not been rebuilt stops receiving updates.")
	}
}

func InitDependencies(ctx context.Context) (*AppContainer, func()) {
	var authRepo services.CliAuthRepository
	var appRepo services.AppRepository
	var branchRepo services.BranchRepository
	var channelRepo services.ChannelRepository
	var updateRepo services.UpdateRepository
	// Stays nil in stateless mode: user accounts only exist on the control
	// plane, the flat-env dashboard authenticates against ADMIN_EMAIL/ADMIN_PASSWORD.
	var userRepo services.UserRepository
	// Nil in stateless mode as well: progressive rollouts are a control-plane
	// feature, and every consumer guards the nil.
	var rolloutRepo services.RolloutRepository
	// Stays nil in stateless mode too: the enterprise license lives in the
	// database, stateless deployments run community edition.
	var licenseRepo licensing.LicenseRepository
	// Same story: the SSO configuration and identities live in the database,
	// so in stateless mode the whole feature is inert.
	var ssoRepo sso.SSORepository
	// And again: per-key access restrictions and branch protection are an
	// enterprise feature backed by the database.
	var apiKeyRestrictionRepo apikeyrestrictions.ApiKeyRestrictionRepository
	// User roles and per-app grants (ee/rbac) live in the database too; nil
	// keeps the whole feature on the community fallback.
	var rbacRepo rbac.RBACRepository
	// The audit trail (ee/audit) as well: nil keeps its recorder a no-op, so
	// stateless deployments never collect an event.
	var auditRepo audit.AuditRepository
	// Device identity (ee/identity) is the Postgres device registry: wired
	// in every DB-mode deployment (no ClickHouse required), it powers the
	// identity dashboard and the update-health display. nil only in
	// stateless mode, where the observe ingestion acknowledge-and-drops and
	// the dashboard handler answers 400. The service owns the store; the
	// ingest route and the dashboard handler both go through it.
	var identityService *identity.Service
	// The ClickHouse side of Observe: telemetry rows and their branch
	// enrichment. Declared as interfaces and only assigned inside the
	// ClickHouse block, so the no-ClickHouse path hands the handler true
	// nils, never a typed-nil interface.
	var telemetrySink observe.TelemetrySink
	var branchResolver observe.BranchResolver
	// Historical health is optional and ClickHouse-backed. Its handler is
	// always wired so no-ClickHouse deployments can report available=false.
	var healthHistory *observe.HealthHistory
	// The fallback series, drawn from PostgreSQL's live state for deployments
	// that run no ClickHouse. It needs the engine, which only exists in control
	// plane mode, so it stays nil elsewhere and the handler says so.
	var stateHistory *observe.StateHistory
	var explorer *observe.Explorer
	// Records device check-ins into the universal registry, debounced; nil
	// only in stateless mode, where polls and ingestion are side-effect free.
	var checkInRecorder *observe.CheckInRecorder

	cleanup := func() {}
	// Releases stack: the last resource opened is the first one closed. Written
	// as a helper because chaining it by hand once per resource is how the
	// order silently drifts.
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

		stateHistory = observe.NewStateHistory(dbEngine)
		authRepo = store.NewPostgresAuthStore(dbEngine)
		appRepo = store.NewPostgresAppStore(dbEngine)
		userRepo = store.NewPostgresUserStore(dbEngine)
		licenseRepo = licensing.NewPostgresLicenseStore(dbEngine)
		ssoRepo = sso.NewPostgresSSOStore(dbEngine)
		apiKeyRestrictionRepo = apikeyrestrictions.NewPostgresApiKeyRestrictionStore(dbEngine)
		rbacRepo = rbac.NewPostgresRBACStore(dbEngine)
		auditRepo = audit.NewPostgresAuditStore(dbEngine)
		branchRepo = store.NewPostgresBranchStore(dbEngine)
		channelRepo = store.NewPostgresChannelStore(dbEngine)
		// Concrete (not the services.UpdateRepository interface): the observe
		// branch resolver borrows its Postgres-only lookup as a method value.
		pgUpdateStore := store.NewPostgresUpdateStore(dbEngine)
		updateRepo = pgUpdateStore
		rolloutRepo = store.NewPostgresRolloutStore(dbEngine)

		// GeoIP enrichment is optional: without a configured GeoLite2 City
		// database, devices simply stay unlocated. A configured-but-broken
		// path fails the boot loudly instead of resolving nothing forever.
		var geoResolver identity.GeoResolver
		if mmdbPath := config.GetEnv("GEOIP_MMDB_PATH"); mmdbPath != "" {
			resolver, err := identity.NewGeoLite2Resolver(mmdbPath)
			if err != nil {
				log.Fatalf("🚨 [IDENTITY] %v", err)
			}
			geoResolver = resolver
			addCleanup(resolver.Close)
		}
		// The device registry (ee/identity) is Postgres-only and runs in
		// EVERY DB-mode deployment: it powers the identity dashboard AND the
		// update-health display (adoption/MAU, launch failures), which must
		// work with no ClickHouse and no observe SDK.
		identityService = identity.NewService(identity.NewPostgresIdentityStore(dbEngine), geoResolver)
		checkInRecorder = observe.NewCheckInRecorder(identityService, cache.GetCache())
		// nil when ClickHouse is absent: the explorer then reduces to its
		// Postgres half rather than disappearing.
		var observeClickHouse *clickhouse.Engine

		// ClickHouse gates ONLY the telemetry pipeline (the metrics/logs
		// fact tables). A configured-but-broken URL fails the boot loudly
		// instead of silently disabling a feature the operator asked for.
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
			// Not a Fatal: deployments without ClickHouse keep booting, the
			// registry and update health work regardless; only the
			// metrics/logs ingestion is off.
			log.Println("⚙️  [OBSERVE] CLICKHOUSE_URL is not set; telemetry ingestion (metrics/logs) stays disabled")
			addCleanup(observe.StartHealthOutboxDiscarder(ctx, dbEngine))
		}
		explorer = observe.NewExplorer(dbEngine, observeClickHouse)
	} else {
		log.Println("⚙️  [STATELESS] Initializing Stateless Mode (Flat-Env Mode)...")
		if err := config.LoadAppsFromFlatEnv(); err != nil {
			log.Fatalf("Invalid apps config: %v\nSee https://mercure-technologies.gitbook.io/expo-open-ota/stateless-mode/getting-started for the stateless (flat-env) config format.", err)
		}
		authRepo = store.NewBucketAuthStore(resolvedBucket)
		appRepo = store.NewBucketAppStore(resolvedBucket)
		branchRepo = store.NewBucketBranchStore(resolvedBucket)
		channelRepo = store.NewBucketChannelStore(resolvedBucket)
		updateRepo = store.NewBucketUpdateStore(resolvedBucket)
	}

	logLegacyAppIdFallback()

	licenseService := licensing.NewLicenseService(licenseRepo)
	// A missing/invalid stored key just means community edition; only an
	// unreachable database is worth a warning, and never a boot failure.
	if err := licenseService.ActivateFromStore(ctx); err != nil {
		log.Printf("⚠️  [LICENSE] Could not load the enterprise license from the database: %v", err)
	}
	// Other replicas learn about license changes through this loop rather
	// than at their next boot.
	licenseService.StartSync(ctx, 30*time.Second)

	apiKeyRestrictionService := apikeyrestrictions.NewApiKeyRestrictionService(apiKeyRestrictionRepo)
	// The audit recorder is handed to every emitting surface below; it
	// no-ops without a control plane and a currently valid license, so the
	// call sites stay unconditional.
	auditService := audit.NewAuditService(auditRepo)
	// The archive and the retention purge read their own configuration: that
	// knowledge is the feature's, not the wiring's. The archive starts first
	// so the purge spares unarchived rows, and an enabled-but-misconfigured
	// archive fails the boot loudly: a compliance archive that silently does
	// not run is worse than a crash.
	if err := auditService.StartArchiveFromEnv(ctx); err != nil {
		log.Fatalf("🚨 [AUDIT] %v", err)
	}
	auditService.StartRetentionPurgeFromEnv(ctx)
	apiKeyRestrictionService.SetOnAuditEvent(auditService.Record)
	rbacService := rbac.NewRBACService(rbacRepo, userRepo)
	rbacService.SetOnAuditEvent(auditService.Record)
	// The community list handlers (apps, settings) receive this method as
	// their AppVisibilityFilter: they filter what a member sees without their
	// package ever importing ee/rbac.
	visibleApps := rbacService.VisibleAppsForPrincipal
	dashboardAuthService := services.NewDashboardAuthService(userRepo)
	// The restriction service doubles as the CLI access policy: every CLI
	// request runs through its enforcement after authenticating.
	cliAuthService := services.NewCliAuthService(authRepo, apiKeyRestrictionService)
	// Only gates the audit actor's key-name lookup: no collection, no lookup.
	cliAuthService.SetAuditActive(auditService.Enabled)
	cliAuthService.SetOnAuditEvent(auditService.Record)
	userService := services.NewUserService(userRepo)
	ssoService := sso.NewSSOService(ssoRepo, userRepo, dashboardAuthService)
	// While SSO is active, members must sign in through it (admins keep the
	// password login as a break-glass access) and accounts arrive through JIT
	// provisioning instead of manual creation.
	dashboardAuthService.SetSSOEnforced(ssoService.Enabled)
	userService.SetSSOEnforced(ssoService.Enabled)
	dashboardAuthService.SetOnAuditEvent(auditService.Record)
	ssoService.SetOnAuditEvent(auditService.Record)
	userService.SetOnAuditEvent(auditService.Record)
	licenseService.SetOnAuditEvent(auditService.Record)
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

	// One limiter shared by every endpoint that accepts a credential. It holds
	// no state of its own, the counters live in the cache, so the same value is
	// safe across handlers and, with Redis configured, across replicas.
	rateLimiter := ratelimit.New(cache.GetCache())

	container := &AppContainer{
		AuthHandler:                 dashhandlers.NewAuthHandler(dashboardAuthService, rateLimiter),
		DashboardAuthService:        dashboardAuthService,
		CliAuthService:              cliAuthService,
		ApiKeyHandler:               dashhandlers.NewApiKeyHandler(cliAuthService),
		ApiKeyRestrictionHandler:    apikeyrestrictions.NewApiKeyRestrictionHandler(apiKeyRestrictionService),
		AppHandler:                  dashhandlers.NewAppHandler(appService, visibleApps),
		AppRepo:                     appRepo,
		BranchHandler:               dashhandlers.NewBranchHandler(branchService),
		ChannelHandler:              dashhandlers.NewChannelHandler(channelService),
		ExpoProtocolHandler:         handlers.NewExpoProtocolHandler(expoProtocolService),
		LicenseHandler:              licensing.NewLicenseHandler(licenseService),
		AuditHandler:                audit.NewAuditHandler(auditService),
		RBACHandler:                 rbac.NewRBACHandler(rbacService),
		RBACService:                 rbacService,
		RepublishHandler:            handlers.NewRepublishHandler(cliAuthService, deploymentService),
		RollbackHandler:             handlers.NewRollbackHandler(cliAuthService, deploymentService),
		RolloutHandler:              dashhandlers.NewRolloutHandler(rolloutService, updateService),
		SettingsHandler:             dashhandlers.NewSettingsHandler(appService, ssoService.Enabled, visibleApps),
		SSOHandler:                  sso.NewSSOHandler(ssoService, rateLimiter),
		UpdateHandler:               dashhandlers.NewUpdateHandler(updateService, deploymentService),
		UploadHandler:               handlers.NewUploadHandler(cliAuthService, deploymentService),
		UsersHandler:                dashhandlers.NewUsersHandler(userService, rateLimiter),
		UserRepo:                    userRepo,
		ObserveIngestHandler:        observe.NewIngestHandler(identityService, telemetrySink, branchResolver, checkInRecorder),
		ObserveHealthHistoryHandler: observe.NewHealthHistoryHandler(healthHistory, stateHistory),
		ObserveExplorerHandler:      observe.NewExplorerHandler(explorer, identityService),
		IdentityHandler:             identity.NewIdentityHandler(identityService),
	}

	// Every manifest poll registers the polling device in the universal
	// device registry (background, debounced); the community fallback is
	// simply "not wired".
	if checkInRecorder != nil {
		container.ExpoProtocolHandler.SetOnDeviceCheckIn(checkInRecorder.Record)
	}

	return container, cleanup
}
