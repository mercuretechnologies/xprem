package infrastructure

import (
	"net/http"

	"github.com/gorilla/mux"
)

// registerClientRoutes registers the expo-updates protocol: a device polls
// /manifest to learn which update it should run, then fetches what that
// manifest points at from /assets. No authentication; the app id is the only
// claim a device makes.
func registerClientRoutes(r *mux.Router, container *AppContainer) {
	r.HandleFunc("/manifest", container.ExpoProtocolHandler.HandleManifest).Methods(http.MethodGet)
	r.HandleFunc("/assets", container.ExpoProtocolHandler.HandleAssets).Methods(http.MethodGet)
}
