package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"xprem/internal/auditlog"

	"github.com/stretchr/testify/require"
)

func TestRequestMetaMiddlewareStampsContext(t *testing.T) {
	var seen auditlog.RequestMeta
	handler := RequestMetaMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = auditlog.MetaFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.RemoteAddr = "203.0.113.9:51234"
	req.Header.Set("User-Agent", "test-agent/1.0")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, "203.0.113.9", seen.IP)
	require.Equal(t, "test-agent/1.0", seen.UserAgent)
}

func TestMetaFromContextOutsideARequest(t *testing.T) {
	// Jobs and tests emit without a request: no network context, no panic.
	require.Zero(t, auditlog.MetaFromContext(context.Background()))
}
