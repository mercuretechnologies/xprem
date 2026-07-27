package test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	infrastructure "expo-open-ota/internal/router"

	"github.com/stretchr/testify/assert"
)

// A trailing slash on an app-scoped route must be a plain 404, never a
// redirect, and the reason is that a redirect on a mutation is worse than a
// refusal. gorilla's StrictSlash answers the slashed form of a route declared
// without one by sending 301, and Go's http.Client and curl -L both rewrite a
// 301 on a non-GET method to GET: DELETE /api/apps/{id}/ came back as a 200
// read, so the caller saw success while nothing had been deleted.
//
// The subrouter carried StrictSlash(true) from when the app root was declared
// as "/" and every caller sent the slashless form. Declaring the root as ""
// closed that gap, which left StrictSlash doing nothing but this. It is off
// now, and this test is what keeps it off: re-adding the line turns every
// assertion below from 404 into 301.
func TestTrailingSlashIsRefusedNotRedirected(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	token := login().Token

	for _, tc := range []struct{ method, path string }{
		// The app root, where the hazard was worst: these two mutate.
		{http.MethodDelete, "/api/apps/test-app-id/"},
		{http.MethodPatch, "/api/apps/test-app-id/"},
		{http.MethodGet, "/api/apps/test-app-id/"},
		// A collection under it, which StrictSlash also used to redirect on a
		// non-GET method, from before this change.
		{http.MethodPost, "/api/apps/test-app-id/branches/"},
		{http.MethodGet, "/api/apps/test-app-id/branches/"},
		{http.MethodGet, "/api/apps/test-app-id/certificate/"},
	} {
		recorder := httptest.NewRecorder()
		req, _ := http.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(recorder, req)

		assert.Equal(t, http.StatusNotFound, recorder.Code,
			"%s %s must refuse outright", tc.method, tc.path)
		assert.Empty(t, recorder.Header().Get("Location"),
			"%s %s must not redirect: a client would re-issue it as GET", tc.method, tc.path)
	}
}

// The other half of the same rule: the slashless form, which is what every
// caller actually sends, still reaches its route. Without this the test above
// would pass just as well on a router that had lost these routes entirely.
func TestSlashlessFormStillMatches(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/apps/test-app-id"},
		{http.MethodDelete, "/api/apps/test-app-id"},
		{http.MethodPatch, "/api/apps/test-app-id"},
		{http.MethodGet, "/api/apps/test-app-id/branches"},
	} {
		recorder := httptest.NewRecorder()
		req, _ := http.NewRequest(tc.method, tc.path, nil)
		// No credential on purpose: 401 proves the ROUTE matched and the auth
		// middleware ran, which is what this test is about. Asserting on a
		// handler's answer would drag in what each handler does.
		router.ServeHTTP(recorder, req)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code,
			"%s %s must match its route", tc.method, tc.path)
	}
}

// The same hazard one level up, and the reason SkipClean(true) is on the root
// router. mux cleans a path carrying a double slash and answers 301 to the
// cleaned form, on EVERY method. Go's http.Client and curl -L rewrite a 301 on
// a non-GET method to GET, so DELETE //api/apps/{id} came back as a 200 read
// and the caller believed the app was gone.
//
// It is one trailing slash away in practice: both clients build their URLs as
// `${baseUrl}/api/...` without normalising, so a BASE_URL ending in a slash
// produces exactly this. Unlike the StrictSlash case this reached every
// mutating route, not just the app root.
func TestDoubleSlashIsRefusedNotRedirected(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	router := infrastructure.NewRouter(testContainer())
	token := login().Token

	for _, tc := range []struct{ method, path string }{
		{http.MethodDelete, "//api/apps/test-app-id"},
		{http.MethodPatch, "//api/apps/test-app-id"},
		{http.MethodDelete, "/api/apps//test-app-id"},
		{http.MethodPost, "//api/apps/test-app-id/branches"},
		{http.MethodPost, "/api/apps/test-app-id//branches"},
		{http.MethodDelete, "//api/apps/test-app-id/branches/branch-1"},
	} {
		recorder := httptest.NewRecorder()
		req, _ := http.NewRequest(tc.method, "http://example.com"+tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(recorder, req)

		assert.Equal(t, http.StatusNotFound, recorder.Code,
			"%s %s must refuse outright", tc.method, tc.path)
		assert.Empty(t, recorder.Header().Get("Location"),
			"%s %s must not redirect: a client would re-issue it as GET", tc.method, tc.path)
	}
}
