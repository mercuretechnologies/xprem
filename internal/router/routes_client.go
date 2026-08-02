package infrastructure

import (
	"net/http"

	"github.com/gorilla/mux"
)

// registerClientRoutes registers the expo-updates protocol: a device polls
// /manifest to learn which update it should run, then fetches what that
// manifest points at from /assets. No authentication; the app id is the only
// claim a device makes.
//
// /branch_lists is not part of the expo-updates protocol: it is what a device
// on a branch-surfing channel reads to know which branches it may ask for.
func registerClientRoutes(r *mux.Router, container *AppContainer) {
	r.HandleFunc("/manifest", container.ExpoProtocolHandler.HandleManifest).Methods(http.MethodGet)
	r.HandleFunc("/assets", container.ExpoProtocolHandler.HandleAssets).Methods(http.MethodGet)
	r.HandleFunc("/branch_lists", container.BranchListHandler.HandleBranchList).Methods(http.MethodGet)
}
