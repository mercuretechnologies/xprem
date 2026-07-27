package infrastructure

import (
	"expo-open-ota/internal/middleware"
	"net/http"

	"github.com/gorilla/mux"
)

// The publish flow, called by the eoas CLI from a developer machine or a CI
// job. These five routes are the whole write path of an update: ask for the
// upload URLs, push the files, seal the update, then the two operations that
// move a branch's pointer afterwards.
//
// AUTHENTICATION: no middleware, and that is deliberate rather than an
// oversight. Every handler validates the credential itself, because what is
// authorised here is a (app, BRANCH) pair and not just an app: branch
// protection is enforced per branch, so the check needs the path variable that
// only the handler is holding. AppResolverMiddleware still runs first and
// turns an unknown APP_ID into a 404 before any of that.
func registerPublishRoutes(r *mux.Router, container *AppContainer) {
	appSubrouter := r.PathPrefix("/{APP_ID}").Subrouter()
	appSubrouter.Use(middleware.AppResolverMiddleware(container.AppRepo))
	appSubrouter.HandleFunc("/requestUploadUrl/{BRANCH}", container.UploadHandler.RequestUploadUrlHandler).Methods(http.MethodPost)
	appSubrouter.HandleFunc("/uploadLocalFile", container.UploadHandler.RequestUploadLocalFileHandler).Methods(http.MethodPut)
	appSubrouter.HandleFunc("/markUpdateAsUploaded/{BRANCH}", container.UploadHandler.MarkUpdateAsUploadedHandler).Methods(http.MethodPost)
	appSubrouter.HandleFunc("/rollback/{BRANCH}", container.RollbackHandler.HandleRollback).Methods(http.MethodPost)
	appSubrouter.HandleFunc("/republish/{BRANCH}", container.RepublishHandler.HandleRepublish).Methods(http.MethodPost)
}
