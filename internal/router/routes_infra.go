package infrastructure

import (
	"expo-open-ota/config"
	"expo-open-ota/internal/metrics"
	"net/http"

	"github.com/gorilla/mux"
)

// Operational surface, called by the orchestrator and the metrics scraper.
//
// AUTHENTICATION: none, on purpose. A liveness probe cannot hold a credential,
// and /metrics is expected to sit behind the network boundary of the cluster
// rather than behind a token. It is registered only when the operator opts in
// with PROMETHEUS_ENABLED, so an untouched deployment does not expose it.
func registerInfraRoutes(r *mux.Router) {
	if config.GetEnv("PROMETHEUS_ENABLED") == "true" {
		r.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			metrics.PrometheusHandler().ServeHTTP(w, r)
		}).Methods(http.MethodGet)
	}

	r.HandleFunc("/hc", HealthCheck).Methods(http.MethodGet)
	// Both routes answer 200 here, and that is correct: this router is only
	// swapped in once the bucket migrations are done, so the pod is by then both
	// alive and ready. The liveness/readiness split happens earlier, in
	// cmd/api/main.go's bootHandler, which registers /hc (200 throughout, so a
	// long migration never gets the pod killed) but deliberately leaves /ready
	// unregistered so it falls into that handler's catch-all 503 and keeps
	// traffic away until this router takes over.
	r.HandleFunc("/ready", HealthCheck).Methods(http.MethodGet)
}
