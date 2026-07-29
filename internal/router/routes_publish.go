package infrastructure

import (
	"expo-open-ota/ee/apikeyrestrictions"
	"expo-open-ota/internal/middleware"
	"net/http"

	"github.com/gorilla/mux"
)

// registerPublishRoutes registers the publish flow called by the eoas CLI:
// request the upload URLs, push the files, seal the update, then rollback or
// republish. Each route declares which action it performs on its {BRANCH},
// which is what an API key's access rules are judged against.
func registerPublishRoutes(r *mux.Router, container *AppContainer) {
	appSubrouter := r.PathPrefix("/{APP_ID}").Subrouter()
	appSubrouter.Use(middleware.AppResolverMiddleware(container.AppRepo))

	publish := publishGroup{
		router:       appSubrouter,
		cliAuth:      container.CliAuthService,
		apiKeyAccess: container.ApiKeyAccessService,
	}

	publish.route(http.MethodPost, "/requestUploadUrl/{BRANCH}", container.UploadHandler.RequestUploadUrlHandler,
		apikeyrestrictions.ActionPublish)
	publish.route(http.MethodPost, "/markUpdateAsUploaded/{BRANCH}", container.UploadHandler.MarkUpdateAsUploadedHandler,
		apikeyrestrictions.ActionPublish)
	publish.route(http.MethodPost, "/rollback/{BRANCH}", container.RollbackHandler.HandleRollback,
		apikeyrestrictions.ActionRollback)
	publish.route(http.MethodPost, "/republish/{BRANCH}", container.RepublishHandler.HandleRepublish,
		apikeyrestrictions.ActionRollback)

	// The local-bucket file upload names no branch in its path; the branch is
	// a claim of the signed token minted by the upload-url request above.
	publish.uploadTokenRoute(http.MethodPut, "/uploadLocalFile", container.UploadHandler.RequestUploadLocalFileHandler)
}
