package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoggingMiddlewareRecoversTelemetryPanics(t *testing.T) {
	for _, path := range []string{
		"/observe/app-1/project-1/v1/logs",
		"/observe/app-1/project-1/v1/metrics",
	} {
		t.Run(path, func(t *testing.T) {
			handler := LoggingMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				panic("boom")
			}))
			request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, nil)
			recorder := httptest.NewRecorder()

			require.NotPanics(t, func() {
				handler.ServeHTTP(recorder, request)
			})
			require.Equal(t, http.StatusInternalServerError, recorder.Code)
		})
	}
}

func TestLoggingMiddlewarePreservesFlush(t *testing.T) {
	// The SSE transport flushes after each event through ResponseController,
	// which must traverse the statusRecorder wrapper (Unwrap) or find Flush.
	handler := LoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: hello\n\n"))
		require.NoError(t, http.NewResponseController(w).Flush())

		_, directlyFlushable := w.(http.Flusher)
		require.True(t, directlyFlushable, "http.Flusher must stay visible through the wrapper")
	}))
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/mcp", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	require.True(t, recorder.Flushed, "the flush must reach the underlying writer")
}
