package infrastructure

import (
	"net/http"
	"xprem/config"
	"xprem/internal/metrics"

	"github.com/gorilla/mux"
)

// registerInfraRoutes registers the operational surface for the orchestrator
// and the metrics scraper. No authentication. /metrics is only registered
// when PROMETHEUS_ENABLED is set.
func registerInfraRoutes(r *mux.Router) {
	if config.GetEnv("PROMETHEUS_ENABLED") == "true" {
		r.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			metrics.PrometheusHandler().ServeHTTP(w, r)
		}).Methods(http.MethodGet)
	}

	r.HandleFunc("/hc", HealthCheck).Methods(http.MethodGet)
	r.HandleFunc("/ready", HealthCheck).Methods(http.MethodGet)
}
