package infrastructure

import (
	"net/http"
	"xprem/ee/apikeyrestrictions"
	"xprem/internal/middleware"

	"github.com/gorilla/mux"
)

// registerBuildRoutes declares the authenticated CLI build inputs and artifact lifecycle.
func registerBuildRoutes(r *mux.Router, container *AppContainer) {
	router := r.PathPrefix("/{APP_ID}/build").Subrouter()
	router.Use(middleware.AppResolverMiddleware(container.AppRepo))

	build := buildGroup{
		router:       router,
		cliAuth:      container.CliAuthService,
		apiKeyAccess: container.ApiKeyAccessService,
		identifiers:  container.AppIdentifierRepo,
	}

	build.route(http.MethodPost, "/{IDENTIFIER_ID}/artifacts/{BUILD_ID}/logs", container.BuildRegistryHandler.AppendLogs, apikeyrestrictions.BuildActionCreate)
	build.route(http.MethodPut, "/{IDENTIFIER_ID}/artifacts/{BUILD_ID}/start", container.BuildRegistryHandler.Start, apikeyrestrictions.BuildActionCreate)
	build.route(http.MethodPut, "/{IDENTIFIER_ID}/artifacts/{BUILD_ID}", container.BuildRegistryHandler.RegisterArtifact, apikeyrestrictions.BuildActionCreate)
	build.route(http.MethodPost, "/{IDENTIFIER_ID}/artifacts/{BUILD_ID}/failed", container.BuildRegistryHandler.Fail, apikeyrestrictions.BuildActionCreate)
	build.route(http.MethodPost, "/{IDENTIFIER_ID}/artifacts/{BUILD_ID}/complete", container.BuildRegistryHandler.Complete, apikeyrestrictions.BuildActionCreate)
	build.route(http.MethodPut, "/{IDENTIFIER_ID}/artifacts/{BUILD_ID}/upload", container.BuildRegistryHandler.UploadLocal, apikeyrestrictions.BuildActionCreate)
	r.HandleFunc("/build-shares/{TOKEN}", container.BuildRegistryHandler.PublicShare).Methods(http.MethodGet)
	deviceRegistrations := r.PathPrefix("/device-registrations").Subrouter()
	deviceRegistrations.Use(middleware.NewDashboardCORSMiddleware())
	deviceRegistrations.Use(middleware.NewReadDeadlineMiddleware(authReadDeadline))
	deviceRegistrations.PathPrefix("/").HandlerFunc(func(http.ResponseWriter, *http.Request) {}).Methods(http.MethodOptions)
	deviceRegistrations.HandleFunc("/{TOKEN}", container.IosCredentialsHandler.PublicIosDeviceInvitationHandler).Methods(http.MethodGet)
	deviceRegistrations.HandleFunc("/{TOKEN}/profile", container.IosCredentialsHandler.IosDeviceRegistrationProfileHandler).Methods(http.MethodGet)
	deviceRegistrations.HandleFunc("/{TOKEN}/enroll", container.IosCredentialsHandler.EnrollIosDeviceHandler).Methods(http.MethodPost)
	deviceRegistrations.HandleFunc("/{TOKEN}/registrations/{REGISTRATION_ID}", container.IosCredentialsHandler.PublicIosDeviceRegistrationHandler).Methods(http.MethodGet)

	build.route(http.MethodGet, "/resolve/{PLATFORM}/{APPLICATION_ID}", container.BuildHandler.ResolveIdentifier,
		apikeyrestrictions.BuildActionCreate)
	build.route(http.MethodPost, "/{IDENTIFIER_ID}/build-number", container.BuildHandler.AllocateBuildNumber,
		apikeyrestrictions.BuildActionCreate)
	build.route(http.MethodGet, "/{IDENTIFIER_ID}/environment", container.BuildHandler.Environment,
		apikeyrestrictions.BuildActionCreate)
	build.route(http.MethodGet, "/{IDENTIFIER_ID}/credentials/android", container.BuildHandler.AndroidCredentials,
		apikeyrestrictions.BuildActionCreate)
}
