package main

import (
	"context"
	"log"
	"net/http"
	"sync/atomic"
	"time"
	"xprem/config"
	"xprem/internal/bucketmigration"
	"xprem/internal/metrics"
	infrastructure "xprem/internal/router"

	_ "xprem/internal/bucketmigrations"
)

func init() {
	config.LoadConfig()
	metrics.InitMetrics()
}

// bootHandler answers while the bucket migrations run, and splits the two probes
// on purpose. /hc (liveness) is registered below and answers 200 throughout, so
// the orchestrator does not kill a pod in the middle of a long migration.
// /ready (readiness) is deliberately NOT registered: it falls into the catch-all
// and answers 503 + Retry-After, which drops the pod from the Service endpoints
// without killing it, so nothing is served from a half-migrated bucket. Every
// other request gets that same 503. Once main swaps in the real router, /ready
// answers 200 like /hc (see internal/router/router.go).
func bootHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/hc", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "15")
		http.Error(w, "storage migration in progress, updates are on hold", http.StatusServiceUnavailable)
	})
	return mux
}

func main() {
	var handler atomic.Pointer[http.Handler]
	boot := bootHandler()
	handler.Store(&boot)

	server := &http.Server{
		Addr: "0.0.0.0:" + config.GetPort(),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			(*handler.Load()).ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// Bind the port before running migrations so /hc answers from the very
	// first probe; ListenAndServe only returns on failure.
	go func() {
		log.Fatalf("Server failed to start: %v", server.ListenAndServe())
	}()
	log.Println("Server is running on port " + config.GetPort())

	// The lock is released on failure, so a crash-looping pod retries the
	// migration on every boot instead of skipping it while the lock expires.
	if err := bucketmigration.EnsureMigrations(); err != nil {
		log.Fatalf("🚨 [BUCKET] %v", err)
	}

	container, cleanup := infrastructure.InitDependencies(context.Background())
	defer cleanup()
	router := infrastructure.NewRouter(container)
	// No global CORS: each surface declares its own where its routes are
	// registered (dashboard subrouters, OAuth endpoints, /mcp).
	ready := http.Handler(router)
	handler.Store(&ready)
	log.Println("✅ Server is ready to serve traffic.")
	select {}
}
