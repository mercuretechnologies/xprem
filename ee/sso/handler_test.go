// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package sso

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"xprem/internal/services"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestHandler(t *testing.T, repo SSORepository, users *fakeUserRepo) (*SSOHandler, *SSOService, *services.DashboardAuthService) {
	t.Helper()
	t.Setenv("USE_DASHBOARD", "true")
	service, sessions := newTestService(t, repo, users)
	// nil limiter: every method is nil-safe and allows the request, which keeps
	// these tests about the SSO flow rather than about throttling.
	return NewSSOHandler(service, nil), service, sessions
}

func flowCookieFrom(t *testing.T, response *http.Response) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if cookie.Name == flowCookieName {
			return cookie
		}
	}
	return nil
}

func fragmentValues(t *testing.T, location string) url.Values {
	t.Helper()
	parsed, err := url.Parse(location)
	require.NoError(t, err)
	values, err := url.ParseQuery(parsed.Fragment)
	require.NoError(t, err)
	return values
}

func TestPublicConfigHandler(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	handler, service, _ := newTestHandler(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	recorder := httptest.NewRecorder()
	handler.GetPublicConfigHandler(recorder, httptest.NewRequest(http.MethodGet, "/auth/sso/config", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.Equal(t, true, payload["enabled"])
	assert.Equal(t, "Test IdP", payload["providerName"])

	// Without a valid license the button disappears: enabled false, and no
	// provider details leak pre-auth.
	service.licenseValid = func() bool { return false }
	recorder = httptest.NewRecorder()
	handler.GetPublicConfigHandler(recorder, httptest.NewRequest(http.MethodGet, "/auth/sso/config", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	payload = map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.Equal(t, false, payload["enabled"])
	assert.NotContains(t, recorder.Body.String(), "Test IdP")
}

func TestSSOHandlersRequireDashboard(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	handler, _, _ := newTestHandler(t, newFakeSSORepo(users, testConfigFor(idp)), users)
	t.Setenv("USE_DASHBOARD", "false")

	for name, handle := range map[string]http.HandlerFunc{
		"config":   handler.GetPublicConfigHandler,
		"login":    handler.LoginRedirectHandler,
		"callback": handler.CallbackHandler,
	} {
		recorder := httptest.NewRecorder()
		handle(recorder, httptest.NewRequest(http.MethodGet, "/auth/sso/"+name, nil))
		assert.Equal(t, http.StatusNotFound, recorder.Code, name)
	}
}

func TestLoginRedirectHandlerSetsFlowCookie(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	handler, _, _ := newTestHandler(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	recorder := httptest.NewRecorder()
	handler.LoginRedirectHandler(recorder, httptest.NewRequest(http.MethodGet, "/auth/sso/login", nil))
	response := recorder.Result()

	require.Equal(t, http.StatusFound, response.StatusCode)
	assert.True(t, strings.HasPrefix(response.Header.Get("Location"), idp.issuer+"/auth?"))

	cookie := flowCookieFrom(t, response)
	require.NotNil(t, cookie, "the flow cookie must be set")
	assert.True(t, cookie.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	assert.Equal(t, "/auth/sso", cookie.Path)
	assert.Equal(t, int(flowTTL.Seconds()), cookie.MaxAge)
	// BASE_URL is plain http in this test, so the cookie must not be Secure
	// (it would never come back on a local deployment).
	assert.False(t, cookie.Secure)
}

func TestLoginRedirectHandlerScopesFlowCookieToBasePath(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	handler, _, _ := newTestHandler(t, newFakeSSORepo(users, testConfigFor(idp)), users)
	t.Setenv("BASE_URL", "https://api.example.com/ota")

	recorder := httptest.NewRecorder()
	handler.LoginRedirectHandler(recorder, httptest.NewRequest(http.MethodGet, "/auth/sso/login", nil))
	cookie := flowCookieFrom(t, recorder.Result())
	require.NotNil(t, cookie)
	assert.Equal(t, "/ota/auth/sso", cookie.Path)
	assert.True(t, cookie.Secure)
}

func TestLoginRedirectHandlerRedirectsErrorsUnderBasePath(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	handler, service, _ := newTestHandler(t, newFakeSSORepo(users, testConfigFor(idp)), users)
	service.licenseValid = func() bool { return false }
	t.Setenv("BASE_URL", "https://api.example.com/ota")

	recorder := httptest.NewRecorder()
	handler.LoginRedirectHandler(recorder, httptest.NewRequest(http.MethodGet, "/auth/sso/login", nil))
	location := recorder.Result().Header.Get("Location")
	assert.True(t, strings.HasPrefix(location, "https://api.example.com/ota/dashboard/login#"))
	assert.Equal(t, ssoErrLicense, fragmentValues(t, location).Get("ssoError"))
}

func TestLoginRedirectHandlerRedirectsErrorsToLoginPage(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	handler, service, _ := newTestHandler(t, newFakeSSORepo(users, testConfigFor(idp)), users)
	service.licenseValid = func() bool { return false }

	recorder := httptest.NewRecorder()
	handler.LoginRedirectHandler(recorder, httptest.NewRequest(http.MethodGet, "/auth/sso/login", nil))
	response := recorder.Result()

	require.Equal(t, http.StatusFound, response.StatusCode)
	location := response.Header.Get("Location")
	assert.True(t, strings.HasPrefix(location, "http://localhost:3000/dashboard/login#"))
	assert.Equal(t, ssoErrLicense, fragmentValues(t, location).Get("ssoError"))
}

// The callback query is attacker-writable pre-auth: whatever lands in the
// logs must have its control characters neutralized (CWE-117 log injection).
func TestSanitizeForLog(t *testing.T) {
	assert.Equal(t, `"forged\n2026/07/18 fake line"`, sanitizeForLog("forged\n2026/07/18 fake line"))
	assert.Equal(t, `"ansi \x1b[31mred"`, sanitizeForLog("ansi \x1b[31mred"))
	long := strings.Repeat("a", 1000)
	sanitized := sanitizeForLog(long)
	assert.Less(t, len(sanitized), 300)
	assert.Contains(t, sanitized, "...")
}

func TestCallbackHandlerMapsIdPDenial(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	handler, _, _ := newTestHandler(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/auth/sso/callback?error=access_denied&error_description=user+cancelled", nil)
	handler.CallbackHandler(recorder, request)
	response := recorder.Result()

	require.Equal(t, http.StatusFound, response.StatusCode)
	assert.Equal(t, ssoErrDenied, fragmentValues(t, response.Header.Get("Location")).Get("ssoError"))
	// The single-use cookie is dropped no matter how the callback ends.
	cleared := flowCookieFrom(t, response)
	require.NotNil(t, cleared)
	assert.Empty(t, cleared.Value)
	assert.Negative(t, cleared.MaxAge)
}

// An unverified email surfaces to the login page as its own fragment code,
// distinct from the generic failure, so the page can explain it.
func TestCallbackHandlerMapsUnverifiedEmail(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	handler, _, sessions := newTestHandler(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	loginRecorder := httptest.NewRecorder()
	handler.LoginRedirectHandler(loginRecorder, httptest.NewRequest(http.MethodGet, "/auth/sso/login", nil))
	loginResponse := loginRecorder.Result()
	cookie := flowCookieFrom(t, loginResponse)
	require.NotNil(t, cookie)
	authURL, err := url.Parse(loginResponse.Header.Get("Location"))
	require.NoError(t, err)
	authQuery := authURL.Query()
	idp.claims = jwt.MapClaims{
		"iss": idp.issuer, "sub": testSubject, "aud": testClientID,
		"exp": time.Now().Add(5 * time.Minute).Unix(), "iat": time.Now().Unix(),
		"nonce": authQuery.Get("nonce"), "email": testEmail,
		// email_verified deliberately absent.
	}

	callbackRecorder := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet,
		"/auth/sso/callback?state="+url.QueryEscape(authQuery.Get("state"))+"&code=test-code", nil)
	callbackRequest.AddCookie(&http.Cookie{Name: flowCookieName, Value: cookie.Value})
	handler.CallbackHandler(callbackRecorder, callbackRequest)
	callbackResponse := callbackRecorder.Result()

	require.Equal(t, http.StatusFound, callbackResponse.StatusCode)
	fragment := fragmentValues(t, callbackResponse.Header.Get("Location"))
	assert.Equal(t, ssoErrEmailUnverified, fragment.Get("ssoError"))
	assert.Empty(t, fragment.Get("ssoToken"))
	_ = sessions
}

func TestCallbackHandlerWithoutCookieFails(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	handler, _, _ := newTestHandler(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	recorder := httptest.NewRecorder()
	handler.CallbackHandler(recorder, httptest.NewRequest(http.MethodGet, "/auth/sso/callback?state=s&code=c", nil))
	response := recorder.Result()

	require.Equal(t, http.StatusFound, response.StatusCode)
	assert.Equal(t, ssoErrFailed, fragmentValues(t, response.Header.Get("Location")).Get("ssoError"))
}

// TestCallbackHandlerHappyPath drives the two handlers exactly like a browser
// would: login redirect (cookie + IdP URL), then the IdP's redirect back, and
// checks the tokens land in the fragment of the login page URL.
func TestCallbackHandlerHappyPath(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	handler, _, sessions := newTestHandler(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	loginRecorder := httptest.NewRecorder()
	handler.LoginRedirectHandler(loginRecorder, httptest.NewRequest(http.MethodGet, "/auth/sso/login", nil))
	loginResponse := loginRecorder.Result()
	require.Equal(t, http.StatusFound, loginResponse.StatusCode)
	cookie := flowCookieFrom(t, loginResponse)
	require.NotNil(t, cookie)

	authURL, err := url.Parse(loginResponse.Header.Get("Location"))
	require.NoError(t, err)
	authQuery := authURL.Query()
	idp.claims = jwt.MapClaims{
		"iss":            idp.issuer,
		"sub":            testSubject,
		"aud":            testClientID,
		"exp":            time.Now().Add(5 * time.Minute).Unix(),
		"iat":            time.Now().Unix(),
		"nonce":          authQuery.Get("nonce"),
		"email":          testEmail,
		"email_verified": true,
	}

	callbackRecorder := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet,
		"/auth/sso/callback?state="+url.QueryEscape(authQuery.Get("state"))+"&code=test-code", nil)
	callbackRequest.AddCookie(&http.Cookie{Name: flowCookieName, Value: cookie.Value})
	handler.CallbackHandler(callbackRecorder, callbackRequest)
	callbackResponse := callbackRecorder.Result()

	require.Equal(t, http.StatusFound, callbackResponse.StatusCode)
	location := callbackResponse.Header.Get("Location")
	assert.True(t, strings.HasPrefix(location, "http://localhost:3000/dashboard/login#"))
	fragment := fragmentValues(t, location)
	require.NotEmpty(t, fragment.Get("ssoToken"))
	require.NotEmpty(t, fragment.Get("ssoRefreshToken"))

	principal, err := sessions.ValidateSession(fragment.Get("ssoToken"))
	require.NoError(t, err)
	assert.Equal(t, testEmail, principal.Email)
	assert.False(t, principal.IsAdmin)
}

func TestAdminConfigHandlers(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	repo := newFakeSSORepo(users, nil)
	handler, service, _ := newTestHandler(t, repo, users)

	// Nothing configured yet: 404 so the dashboard shows the empty form.
	recorder := httptest.NewRecorder()
	handler.GetConfigHandler(recorder, httptest.NewRequest(http.MethodGet, "/api/sso", nil))
	assert.Equal(t, http.StatusNotFound, recorder.Code)

	// A save against the fake IdP persists and echoes the masked view.
	body := `{"issuer":"` + idp.issuer + `","clientId":"test-client-id","clientSecret":"super-secret","providerName":"Microsoft","enabled":true,"allowedEmailDomains":["@Acme.com"],"allowedGroups":["eng"],"trustUnverifiedEmail":true}`
	recorder = httptest.NewRecorder()
	handler.SaveConfigHandler(recorder, httptest.NewRequest(http.MethodPut, "/api/sso", strings.NewReader(body)))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "super-secret")
	var saved map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &saved))
	assert.Equal(t, true, saved["hasClientSecret"])
	assert.Equal(t, []any{"acme.com"}, saved["allowedEmailDomains"])
	assert.Equal(t, true, saved["trustUnverifiedEmail"])

	// The GET never carries the secret either, in any field.
	recorder = httptest.NewRecorder()
	handler.GetConfigHandler(recorder, httptest.NewRequest(http.MethodGet, "/api/sso", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "super-secret")
	assert.Contains(t, recorder.Body.String(), `"hasClientSecret":true`)
	assert.Contains(t, recorder.Body.String(), `"redirectUri":"http://localhost:3000/auth/sso/callback"`)

	// Validation failures answer 400 with the actionable reason.
	recorder = httptest.NewRecorder()
	handler.SaveConfigHandler(recorder, httptest.NewRequest(http.MethodPut, "/api/sso",
		strings.NewReader(`{"issuer":"http://not-loopback.example.com","clientId":"x","clientSecret":"y"}`)))
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "must use https")

	// Mutations are license-gated; reads are not.
	service.licenseValid = func() bool { return false }
	recorder = httptest.NewRecorder()
	handler.SaveConfigHandler(recorder, httptest.NewRequest(http.MethodPut, "/api/sso", strings.NewReader(body)))
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	recorder = httptest.NewRecorder()
	handler.DeleteConfigHandler(recorder, httptest.NewRequest(http.MethodDelete, "/api/sso", nil))
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	recorder = httptest.NewRecorder()
	handler.GetConfigHandler(recorder, httptest.NewRequest(http.MethodGet, "/api/sso", nil))
	assert.Equal(t, http.StatusOK, recorder.Code)

	// With the license back, delete empties the configuration.
	service.licenseValid = func() bool { return true }
	recorder = httptest.NewRecorder()
	handler.DeleteConfigHandler(recorder, httptest.NewRequest(http.MethodDelete, "/api/sso", nil))
	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Nil(t, repo.cfg)
}

func TestAdminConfigHandlersInStatelessMode(t *testing.T) {
	users := newFakeUserRepo()
	handler, _, _ := newTestHandler(t, nil, users)

	recorder := httptest.NewRecorder()
	handler.GetConfigHandler(recorder, httptest.NewRequest(http.MethodGet, "/api/sso", nil))
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "stateless mode")
}
