package infrastructure

import (
	"net/http"

	"github.com/gorilla/mux"
)

// The expo-updates protocol, called by expo-updates inside every installed
// app. A device polls /manifest to learn which update it should be running,
// then fetches what that manifest points at from /assets.
//
// AUTHENTICATION: none, because the protocol has none. expo-updates sends
// headers describing the device (its channel, its runtime version, the update
// it is currently running) and no credential at all, so what identifies the
// caller is the app id and nothing more. The handlers answer from that alone,
// and code signing is what lets the device verify the answer rather than the
// other way round.
func registerClientRoutes(r *mux.Router, container *AppContainer) {
	r.HandleFunc("/manifest", container.ExpoProtocolHandler.HandleManifest).Methods(http.MethodGet)
	r.HandleFunc("/assets", container.ExpoProtocolHandler.HandleAssets).Methods(http.MethodGet)
}
