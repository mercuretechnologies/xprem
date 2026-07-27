package middleware

import (
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/mux"
)

// NewReadDeadlineMiddleware bounds how long a request may take to send its
// body, per route rather than for the whole server.
//
// The server sets ReadHeaderTimeout, which stops a client from dribbling
// headers, but nothing bounds the body: a caller that sends one byte of a
// 16MB payload every few seconds holds a connection and a goroutine for as
// long as it likes, and enough of them exhaust both. That is slowloris, and
// the endpoint it matters on is the public unauthenticated one.
//
// Not set globally on http.Server, which would be the obvious place, because
// /{APP_ID}/uploadLocalFile streams a whole update bundle: a deadline short
// enough to be useful here would refuse an honest upload over a slow link.
// So each route says what it needs, and only the routes that read a bounded
// body in one shot take a bound.
func NewReadDeadlineMiddleware(deadline time.Duration) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// ErrNotSupported is what a ResponseWriter that cannot carry a
			// deadline returns, httptest.ResponseRecorder among them. Ignored
			// rather than failed: a handler that works under a real server
			// must not stop working under a test one.
			err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(deadline))
			if err != nil && !errors.Is(err, http.ErrNotSupported) {
				http.Error(w, "Could not bound the request", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
