package middleware

import (
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

func redactHeaders(headers http.Header) http.Header {
	redactedHeaders := make(http.Header)
	for key, values := range headers {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "expo-session") || strings.EqualFold(key, "X-Expo-Access-Token") {
			redactedHeaders[key] = []string{"REDACTED"}
		} else {
			redactedHeaders[key] = values
		}
	}
	return redactedHeaders
}

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hc" || r.URL.Path == "/metrics" || r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		safeHeaders := redactHeaders(r.Header)
		defer func() {
			if err := recover(); err != nil {
				log.Printf("Panic recovered during %s %s\nQuery: %s\nHeaders: %v\nError: %v\nStack Trace:\n%s",
					r.Method, r.RequestURI, r.URL.RawQuery, safeHeaders, err, debug.Stack())
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()

		// Telemetry ingestion fires on every app-background of every device;
		// logging each batch would drown the request log.
		if strings.HasSuffix(r.URL.Path, "/v1/logs") || strings.HasSuffix(r.URL.Path, "/v1/metrics") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()

		log.Printf("Started %s %s with query: %s and headers: %v", r.Method, r.RequestURI, r.URL.RawQuery, safeHeaders)

		recorder := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(recorder, r)

		if recorder.statusCode >= 500 {
			log.Printf("Error detected: %s %s returned status %d", r.Method, r.RequestURI, recorder.statusCode)
		}
		log.Printf("Completed %s %d in %v", r.RequestURI, recorder.statusCode, time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
