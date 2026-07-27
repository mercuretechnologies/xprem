package infrastructure

import (
	"expo-open-ota/ee/observe"
	"net/http"

	"github.com/gorilla/mux"
)

// expo-observe ingestion (ee/observe), called by the SDK running inside every
// installed app, all under one /observe prefix. The operator sets
// extra.eas.observe.endpointUrl to https://<host>/observe/{APP_ID}; the SDK
// appends /{projectId}/v1/logs with the app's REAL EAS project id (used by EAS
// builds, never equal to our APP_ID), so the PROJECT_ID segment is deliberately
// ignored, exactly as the SDK itself never validates it. Exact paths, no
// trailing-slash variant: a gorilla 301 would turn the POST into a bodyless
// GET.
//
// AUTHENTICATION: none, and there is no way around that. The caller is an
// installed mobile app, so any secret shipped to it is a secret every user
// holds. Naming an app id is the only claim a device makes, and the app check
// below is what stops that claim from being a free database read. What guards
// this route is therefore not a credential but the bounds the handlers apply
// to a body: a size ceiling, a record ceiling, and per-batch ceilings on the
// database work one request may order.
func registerIngestRoutes(r *mux.Router, container *AppContainer) {
	observeSubrouter := r.PathPrefix("/observe/{APP_ID}").Subrouter()
	// The app check is memoized so telemetry (which fires on every
	// app-background of every device) does not issue an uncached primary-key
	// query per request.
	observeSubrouter.Use(observe.CachedAppResolverMiddleware(container.AppRepo))
	observeSubrouter.HandleFunc("/{PROJECT_ID}/v1/logs", container.ObserveIngestHandler.HandleLogs).Methods(http.MethodPost)
	observeSubrouter.HandleFunc("/{PROJECT_ID}/v1/metrics", container.ObserveIngestHandler.HandleMetrics).Methods(http.MethodPost)
}
