package middleware

import (
	"bytes"
	"context"
	"log"
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

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buffer)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buffer
}

const uploadGrant = "eyJhbGciOiJIUzI1NiJ9.UPLOADGRANT.sig"

func TestLoggingMiddlewareRedactsLocalUploadHeader(t *testing.T) {
	for _, target := range []string{"/app-1/build/identifier-1/artifacts/build-1/upload", "/app-1/uploadLocalFile"} {
		t.Run(target, func(t *testing.T) {
			for _, header := range []string{"local-upload-token", "Local-Upload-Token", "LOCAL-UPLOAD-TOKEN"} {
				for _, panics := range []bool{false, true} {
					logs := captureLogs(t)
					handler := LoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						require.Equal(t, []string{uploadGrant}, r.Header[header], "logging must not alter the request")
						if panics {
							panic("upload failed")
						}
						w.WriteHeader(http.StatusNoContent)
					}))
					request := httptest.NewRequest(http.MethodPut, target, nil)
					request.Header[header] = []string{uploadGrant}
					request.Header.Set("Authorization", "Bearer eoo-secret")
					request.Header.Set("X-Expo-Access-Token", "expo-secret")
					request.Header.Set("Expo-Session", "session-secret")
					request.Header.Set("Cookie", "session=cookie-secret")
					request.Header.Set("User-Agent", "eoas/2.0")
					handler.ServeHTTP(httptest.NewRecorder(), request)
					require.Contains(t, logs.String(), "REDACTED")
					require.NotContains(t, logs.String(), uploadGrant)
					require.NotContains(t, logs.String(), "eoo-secret")
					require.NotContains(t, logs.String(), "expo-secret")
					require.NotContains(t, logs.String(), "session-secret")
					require.NotContains(t, logs.String(), "cookie-secret")
					require.Contains(t, logs.String(), "eoas/2.0", "ordinary headers stay visible")
				}
			}
		})
	}
}

func TestLoggingMiddlewareKeepsOrdinaryQueries(t *testing.T) {
	logs := captureLogs(t)
	handler := LoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodGet, "/app-1/build/id-1/environment?channel=production", nil)
	request.Header.Set("Authorization", "Bearer eoo_secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	output := logs.String()
	require.Contains(t, output, "channel=production")
	require.Contains(t, output, "/app-1/build/id-1/environment?channel=production")
	require.NotContains(t, output, "/[REDACTED]")
	require.NotContains(t, output, "?[REDACTED]")
	require.NotContains(t, output, "eoo_secret")
	require.Contains(t, output, "Authorization:[REDACTED]")
}

const shareToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestLoggingMiddlewareRedactsBuildShares(t *testing.T) {
	for _, tc := range []struct {
		name, method, target string
		status               int
	}{
		{"share page", http.MethodGet, "/build-shares/" + shareToken, http.StatusOK},
		{"encoded slash", http.MethodGet, "/build-shares%2F" + shareToken, http.StatusOK},
		{"encoded route", http.MethodGet, "/build-%73hares/" + shareToken, http.StatusOK},
		{"encoded query link", http.MethodGet, "/api?next=%2Fbuild-shares%2F" + shareToken, http.StatusOK},
		{"share download", http.MethodGet, "/build-shares/" + shareToken + "/download", http.StatusOK},
		{"share under sub path", http.MethodGet, "/ota/build-shares/" + shareToken + "/download", http.StatusOK},
		{"server error", http.MethodGet, "/build-shares/" + shareToken, http.StatusInternalServerError},
		{"token in another query", http.MethodGet, "/api/app/app-1?next=/build-shares/" + shareToken, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			handler := LoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			request := httptest.NewRequest(tc.method, tc.target, nil)
			request.Header.Set("Referer", "https://ota.example.com/build-shares/"+shareToken)
			request.Header.Set("Authorization", "Bearer eoo_secret")
			request.Header.Set("Expo-Session", "session-secret")
			request.Header.Set("Cookie", "session=cookie-secret")
			request.Header.Set("User-Agent", "eoas/2.0")
			request.Header.Set("X-Link", "https://ota.example.com/build-shares/"+shareToken+"?echo="+shareToken)
			request.Header.Set("X-Encoded-Link", "https://ota.example.com/build-shares%2F"+shareToken)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			require.Equal(t, tc.status, recorder.Code)
			output := logs.String()
			require.Contains(t, output, "Started "+tc.method)
			require.Contains(t, output, "Completed")
			require.Contains(t, output, "[REDACTED]")
			require.Contains(t, output, "eoas/2.0", "ordinary headers stay visible")
			for _, secret := range []string{shareToken, "eoo_secret", "session-secret", "cookie-secret"} {
				require.NotContains(t, output, secret)
			}
			if tc.status >= 500 {
				require.Contains(t, output, "Error detected")
			}
		})
	}
}

func TestLoggingMiddlewareRedactsShareInPanic(t *testing.T) {
	logs := captureLogs(t)
	handler := LoggingMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("share failed")
	}))
	request := httptest.NewRequest(http.MethodGet, "/build-shares/"+shareToken+"?x=1", nil)
	request.Header.Set("Referer", "https://ota.example.com/build-shares/"+shareToken)
	recorder := httptest.NewRecorder()
	require.NotPanics(t, func() { handler.ServeHTTP(recorder, request) })
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	output := logs.String()
	require.Contains(t, output, "Panic recovered")
	require.Contains(t, output, "/build-shares/[REDACTED]")
	require.NotContains(t, output, shareToken)
}

func TestRedactCapability(t *testing.T) {
	require.Equal(t, "/build-shares/[REDACTED]/download", redactCapability("/build-shares/"+shareToken+"/download"))
	require.Equal(t, "https://h/x/build-shares/[REDACTED]?[REDACTED]", redactCapability("https://h/x/build-shares/"+shareToken+"?a=1"))
	require.Equal(t, "/build-shares/", redactCapability("/build-shares/"))
	require.Equal(t, "/api/app/app-1/builds/b-1/shares", redactCapability("/api/app/app-1/builds/b-1/shares"))
	require.Equal(t, "[REDACTED]", redactCapability("%2Fbuild-shares%2F"+shareToken))
	require.Equal(t, "[REDACTED]", redactCapability("%252Fbuild-shares%252F"+shareToken))
}

const deviceRegistrationToken = "Zm9vYmFyYmF6cXV4Zm9vYmFyYmF6cXV4Zm9vYmFyYmF6"

func TestLoggingMiddlewareRedactsDeviceRegistrationLinks(t *testing.T) {
	for _, target := range []string{
		"/device-registrations/" + deviceRegistrationToken,
		"/device-registrations/" + deviceRegistrationToken + "/profile",
		"/device-registrations/" + deviceRegistrationToken + "/enroll",
		"/device-registrations/" + deviceRegistrationToken + "/registrations/11111111-1111-1111-1111-111111111111",
		"/dashboard/register-device/" + deviceRegistrationToken + "?registration=11111111-1111-1111-1111-111111111111",
		"/dashboard/register-device%2F" + deviceRegistrationToken,
	} {
		t.Run(target, func(t *testing.T) {
			logs := captureLogs(t)
			handler := LoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusMovedPermanently)
			}))
			request := httptest.NewRequest(http.MethodGet, target, nil)
			request.Header.Set("Referer", "https://ota.example.com/dashboard/register-device/"+deviceRegistrationToken)
			request.Header.Set("User-Agent", "iPhone")
			handler.ServeHTTP(httptest.NewRecorder(), request)
			output := logs.String()
			require.Contains(t, output, "[REDACTED]")
			require.Contains(t, output, "iPhone", "ordinary headers stay visible")
			require.NotContains(t, output, deviceRegistrationToken)
		})
	}
}
