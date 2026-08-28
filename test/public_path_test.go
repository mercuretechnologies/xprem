package test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	infrastructure "xprem/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicPathMountsTheApp(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	t.Setenv("BASE_URL", "http://localhost:3000/ota")
	t.Setenv("SERVE_FROM_SUB_PATH", "true")
	t.Setenv("DASHBOARD_ROOT_REDIRECT", "true")
	router := infrastructure.NewRouter(testContainer())

	hc := httptest.NewRecorder()
	router.ServeHTTP(hc, httptest.NewRequest(http.MethodGet, "/hc", nil))
	assert.Equal(t, http.StatusOK, hc.Code)

	prefixedHc := httptest.NewRecorder()
	router.ServeHTTP(prefixedHc, httptest.NewRequest(http.MethodGet, "/ota/hc", nil))
	assert.Equal(t, http.StatusOK, prefixedHc.Code)

	unprefixed := httptest.NewRecorder()
	router.ServeHTTP(unprefixed, httptest.NewRequest(http.MethodGet, "/manifest", nil))
	assert.Equal(t, http.StatusNotFound, unprefixed.Code)

	manifest := httptest.NewRecorder()
	router.ServeHTTP(manifest, httptest.NewRequest(http.MethodGet, "/ota/manifest", nil))
	assert.NotEqual(t, http.StatusNotFound, manifest.Code)

	root := httptest.NewRecorder()
	router.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/ota", nil))
	assert.Equal(t, http.StatusFound, root.Code)
	assert.Equal(t, "/ota/dashboard/", root.Header().Get("Location"))

	slash := httptest.NewRecorder()
	router.ServeHTTP(slash, httptest.NewRequest(http.MethodGet, "/ota/", nil))
	assert.Equal(t, http.StatusFound, slash.Code)
	assert.Equal(t, "/ota/dashboard/", slash.Header().Get("Location"))

	env := httptest.NewRecorder()
	router.ServeHTTP(env, httptest.NewRequest(http.MethodGet, "/ota/dashboard/env.js", nil))
	assert.Equal(t, http.StatusOK, env.Code)
	assert.Contains(t, env.Body.String(), `"VITE_OTA_API_URL":"http://localhost:3000/ota"`)
	assert.Contains(t, env.Body.String(), `"DASHBOARD_BASENAME":"/ota/dashboard"`)

	sso := httptest.NewRecorder()
	router.ServeHTTP(sso, httptest.NewRequest(http.MethodGet, "/ota/auth/sso/config", nil))
	assert.NotEqual(t, http.StatusNotFound, sso.Code)
}

func TestNestedPublicPathMountsTheApp(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	t.Setenv("BASE_URL", "https://api.example.com/path1/path2")
	t.Setenv("SERVE_FROM_SUB_PATH", "true")
	t.Setenv("DASHBOARD_ROOT_REDIRECT", "true")
	router := infrastructure.NewRouter(testContainer())

	hc := httptest.NewRecorder()
	router.ServeHTTP(hc, httptest.NewRequest(http.MethodGet, "/path1/path2/hc", nil))
	assert.Equal(t, http.StatusOK, hc.Code)

	root := httptest.NewRecorder()
	router.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/path1/path2", nil))
	assert.Equal(t, http.StatusFound, root.Code)
	assert.Equal(t, "/path1/path2/dashboard/", root.Header().Get("Location"))

	manifest := httptest.NewRecorder()
	router.ServeHTTP(manifest, httptest.NewRequest(http.MethodGet, "/path1/path2/manifest", nil))
	assert.NotEqual(t, http.StatusNotFound, manifest.Code)

	env := httptest.NewRecorder()
	router.ServeHTTP(env, httptest.NewRequest(http.MethodGet, "/path1/path2/dashboard/env.js", nil))
	require.Equal(t, http.StatusOK, env.Code)
	assert.Contains(t, env.Body.String(), `"DASHBOARD_BASENAME":"/path1/path2/dashboard"`)
}

// Default mode: the reverse proxy strips BASE_URL's path, so routes stay at
// the process root while emitted links still carry the public prefix.
func TestStripModeKeepsRootRoutesAndPublicLinks(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	t.Setenv("BASE_URL", "https://api.example.com/ota")
	t.Setenv("DASHBOARD_ROOT_REDIRECT", "true")
	router := infrastructure.NewRouter(testContainer())

	manifest := httptest.NewRecorder()
	router.ServeHTTP(manifest, httptest.NewRequest(http.MethodGet, "/manifest", nil))
	assert.NotEqual(t, http.StatusNotFound, manifest.Code)

	root := httptest.NewRecorder()
	router.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusFound, root.Code)
	assert.Equal(t, "/ota/dashboard/", root.Header().Get("Location"))

	env := httptest.NewRecorder()
	router.ServeHTTP(env, httptest.NewRequest(http.MethodGet, "/dashboard/env.js", nil))
	require.Equal(t, http.StatusOK, env.Code)
	assert.Contains(t, env.Body.String(), `"DASHBOARD_BASENAME":"/ota/dashboard"`)
}
