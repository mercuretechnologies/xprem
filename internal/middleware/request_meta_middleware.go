package middleware

import (
	"net/http"
	"xprem/internal/auditlog"
	"xprem/internal/helpers"
)

// RequestMetaMiddleware stamps the client IP (proxy-aware, see
// helpers.ClientIP) and user agent on every request's context, where audit
// events emitted from any layer below pick them up without touching the
// request.
func RequestMetaMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta := auditlog.RequestMeta{UserAgent: r.UserAgent()}
		if addr := helpers.ClientIP(r); addr.IsValid() {
			meta.IP = addr.String()
		}
		next.ServeHTTP(w, r.WithContext(auditlog.WithRequestMeta(r.Context(), meta)))
	})
}
