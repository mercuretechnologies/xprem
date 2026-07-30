package middleware

import (
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/mux"
)

// NewReadDeadlineMiddleware bounds how long a request may take to send its
// body, per route rather than for the whole server: a route that streams a
// large upload needs a longer or absent bound than one reading a small batch.
func NewReadDeadlineMiddleware(deadline time.Duration) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// ErrNotSupported is what a ResponseWriter that cannot carry a
			// deadline returns (e.g. httptest.ResponseRecorder); ignored rather
			// than failed.
			err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(deadline))
			if err != nil && !errors.Is(err, http.ErrNotSupported) {
				http.Error(w, "Could not bound the request", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
