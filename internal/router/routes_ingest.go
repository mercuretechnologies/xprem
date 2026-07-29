package infrastructure

import (
	"expo-open-ota/ee/observe"
	"expo-open-ota/internal/middleware"
	"net/http"
	"time"

	"github.com/gorilla/mux"
)

// ingestReadDeadline bounds how long a batch may take to arrive.
const ingestReadDeadline = 30 * time.Second

// registerIngestRoutes registers expo-observe ingestion (ee/observe), called
// by the SDK running inside every installed app. No authentication: an app id
// is the only claim a device makes; the PROJECT_ID path segment is ignored.
func registerIngestRoutes(r *mux.Router, container *AppContainer) {
	observeSubrouter := r.PathPrefix("/observe/{APP_ID}").Subrouter()
	observeSubrouter.Use(observe.CachedAppResolverMiddleware(container.AppRepo))
	observeSubrouter.Use(middleware.NewReadDeadlineMiddleware(ingestReadDeadline))
	observeSubrouter.HandleFunc("/{PROJECT_ID}/v1/logs", container.ObserveIngestHandler.HandleLogs).Methods(http.MethodPost)
	observeSubrouter.HandleFunc("/{PROJECT_ID}/v1/metrics", container.ObserveIngestHandler.HandleMetrics).Methods(http.MethodPost)
}
