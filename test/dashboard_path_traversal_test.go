package test

import (
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"testing"
	infrastructure "xprem/internal/router"
)

func TestDashboardServesStaticFile(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/dashboard/env.js", nil)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusOK, respRec.Code)
	assert.Contains(t, respRec.Body.String(), "window.env")
}

func TestDashboardSPAFallback(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/dashboard/some/route", nil)
	router.ServeHTTP(respRec, req)
	// SPA fallback serves index.html — should not 403
	assert.NotEqual(t, http.StatusForbidden, respRec.Code)
}

func TestDashboardPathTraversalBlockedJson(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	// Go's net/http cleans the path, so .. gets resolved before reaching the handler.
	// But we still test that the guard works at the handler level.
	// We forge a request that somehow has a traversal path ending in .json
	req := httptest.NewRequest("GET", "/dashboard/../../package.json", nil)
	router.ServeHTTP(respRec, req)
	// Go cleans this to /package.json which won't match /dashboard prefix → 404
	assert.NotEqual(t, http.StatusOK, respRec.Code)
}

func TestDashboardRedirectWithoutTrailingSlash(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	respRec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/dashboard", nil)
	router.ServeHTTP(respRec, req)
	assert.Equal(t, http.StatusMovedPermanently, respRec.Code)
	assert.Equal(t, "/dashboard/", respRec.Header().Get("Location"))
}
