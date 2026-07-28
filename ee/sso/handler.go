// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package sso

import (
	"encoding/json"
	"errors"
	"expo-open-ota/config"
	"expo-open-ota/internal/dashboard"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/ratelimit"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const flowCookieName = "eoo_sso_flow"

// Fragment error codes handed back to the login page. The underlying error is
// always logged server-side and never travels in the URL: the code is just
// enough for the page to pick a message.
const (
	ssoErrDenied          = "sso_denied"
	ssoErrLicense         = "sso_license"
	ssoErrEmailMissing    = "sso_email_missing"
	ssoErrEmailUnverified = "sso_email_unverified"
	ssoErrForbidden       = "sso_forbidden"
	ssoErrPending         = "sso_pending"
	ssoErrFailed          = "sso_failed"
	ssoErrThrottled       = "sso_throttled"
)

type SSOHandler struct {
	service *SSOService
	limiter *ratelimit.Limiter
}

func NewSSOHandler(service *SSOService, limiter *ratelimit.Limiter) *SSOHandler {
	return &SSOHandler{service: service, limiter: limiter}
}

// renderSSOServiceError maps the service's business errors onto explicit
// status codes for the admin endpoints; anything unrecognized stays an
// opaque 500.
func renderSSOServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrSSORequiresControlPlane), errors.Is(err, ErrClientSecretUnreadable):
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrSSORequiresValidLicense):
		handlers.RenderError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrSSONotConfigured):
		handlers.RenderError(w, http.StatusNotFound, err.Error())
	default:
		if validationErr := (*ConfigValidationError)(nil); errors.As(err, &validationErr) {
			handlers.RenderError(w, http.StatusBadRequest, validationErr.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
	}
}

// ssoErrorCode picks the fragment code for a failed sign-in flow.
func ssoErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrSSORequiresValidLicense):
		return ssoErrLicense
	case errors.Is(err, ErrSSOEmailMissing):
		return ssoErrEmailMissing
	case errors.Is(err, ErrSSOEmailUnverified):
		return ssoErrEmailUnverified
	case errors.Is(err, ErrSSOAccessRestricted):
		return ssoErrForbidden
	case errors.Is(err, ErrSSOAccountPendingApproval):
		return ssoErrPending
	default:
		return ssoErrFailed
	}
}

// sanitizeForLog neutralizes values echoed from the public callback URL
// before they reach the logs: query.Get percent-decodes, so without this an
// unauthenticated caller could inject newlines (forged log lines) or terminal
// escape sequences. Quoting escapes every control character; the cap keeps a
// crafted query from flooding the log file.
func sanitizeForLog(value string) string {
	const maxLoggedLen = 256
	if len(value) > maxLoggedLen {
		value = value[:maxLoggedLen] + "..."
	}
	return strconv.Quote(value)
}

// loginPageURL builds the dashboard login URL carrying values in the URL
// fragment: fragments never reach a server, so neither tokens nor error codes
// can end up in access logs anywhere.
func loginPageURL(fragment url.Values) string {
	return strings.TrimRight(config.GetEnv("BASE_URL"), "/") + "/dashboard/login#" + fragment.Encode()
}

func redirectWithError(w http.ResponseWriter, r *http.Request, code string) {
	fragment := url.Values{}
	fragment.Set("ssoError", code)
	http.Redirect(w, r, loginPageURL(fragment), http.StatusFound)
}

// flowCookie carries the signed flow token between the login redirect and the
// callback. Path-scoped to the sso routes, HttpOnly, and Lax so the top-level
// redirect back from the IdP still sends it.
func flowCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     flowCookieName,
		Value:    value,
		Path:     "/auth/sso",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(config.GetEnv("BASE_URL"), "https://"),
	}
}

// GetPublicConfigHandler is the pre-auth endpoint the login page polls to
// decide whether to render the SSO button. It always answers 200: any reason
// SSO is unavailable simply reads as {"enabled": false}.
func (h *SSOHandler) GetPublicConfigHandler(w http.ResponseWriter, r *http.Request) {
	if !dashboard.IsDashboardEnabled() {
		handlers.RenderError(w, http.StatusNotFound, "Dashboard is disabled")
		return
	}
	handlers.RenderJSON(w, http.StatusOK, h.service.PublicConfig(r.Context()))
}

// LoginRedirectHandler starts the flow: it drops the flow cookie and sends
// the browser to the IdP's authorization endpoint.
func (h *SSOHandler) LoginRedirectHandler(w http.ResponseWriter, r *http.Request) {
	if !dashboard.IsDashboardEnabled() {
		handlers.RenderError(w, http.StatusNotFound, "Dashboard is disabled")
		return
	}
	redirect, err := h.service.BeginLogin(r.Context())
	if err != nil {
		log.Printf("[SSO] could not start the sign-in flow: %v", err)
		redirectWithError(w, r, ssoErrorCode(err))
		return
	}
	http.SetCookie(w, flowCookie(redirect.FlowToken, int(flowTTL.Seconds())))
	http.Redirect(w, r, redirect.AuthURL, http.StatusFound)
}

// CallbackHandler completes the flow when the IdP redirects back, then hands
// the session pair to the login page in the URL fragment.
func (h *SSOHandler) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	if !dashboard.IsDashboardEnabled() {
		handlers.RenderError(w, http.StatusNotFound, "Dashboard is disabled")
		return
	}
	// The flow token is single-use: whatever happens next, the cookie dies here.
	http.SetCookie(w, flowCookie("", -1))
	// The state parameter is already single-use, so this is not about guessing
	// a secret, it is about the volume of failed round-trips one address can
	// force the server to run: every one of them costs a token exchange and a
	// userinfo call against the identity provider.
	clientIP := helpers.ClientIP(r)
	if decision := h.limiter.CheckSSOCallback(clientIP); !decision.Allowed {
		redirectWithError(w, r, ssoErrThrottled)
		return
	}
	query := r.URL.Query()
	if idpError := query.Get("error"); idpError != "" {
		log.Printf("[SSO] the identity provider returned an error: %s (%s)", sanitizeForLog(idpError), sanitizeForLog(query.Get("error_description")))
		code := ssoErrFailed
		if idpError == "access_denied" {
			code = ssoErrDenied
		}
		redirectWithError(w, r, code)
		return
	}
	cookie, err := r.Cookie(flowCookieName)
	if err != nil || cookie.Value == "" {
		log.Printf("[SSO] callback received without a flow cookie (sign-in took longer than %s, or cookies are blocked)", flowTTL)
		h.limiter.RecordSSOCallbackFailure(clientIP)
		redirectWithError(w, r, ssoErrFailed)
		return
	}
	state, code := query.Get("state"), query.Get("code")
	if state == "" || code == "" {
		log.Printf("[SSO] callback received without a state or code parameter")
		h.limiter.RecordSSOCallbackFailure(clientIP)
		redirectWithError(w, r, ssoErrFailed)
		return
	}
	session, err := h.service.CompleteLogin(r.Context(), cookie.Value, state, code)
	if err != nil {
		log.Printf("[SSO] sign-in failed: %v", err)
		h.limiter.RecordSSOCallbackFailure(clientIP)
		redirectWithError(w, r, ssoErrorCode(err))
		return
	}
	fragment := url.Values{}
	fragment.Set("ssoToken", session.Token)
	fragment.Set("ssoRefreshToken", session.RefreshToken)
	http.Redirect(w, r, loginPageURL(fragment), http.StatusFound)
}

// adminConfigResponse is the JSON shape of the admin card. The client secret
// itself never appears; hasClientSecret drives the placeholder in the form.
type adminConfigResponse struct {
	Issuer               string   `json:"issuer"`
	ClientID             string   `json:"clientId"`
	HasClientSecret      bool     `json:"hasClientSecret"`
	ProviderName         string   `json:"providerName"`
	Scopes               string   `json:"scopes"`
	Enabled              bool     `json:"enabled"`
	AllowedEmailDomains  []string `json:"allowedEmailDomains"`
	AllowedGroups        []string `json:"allowedGroups"`
	GroupsClaim          string   `json:"groupsClaim"`
	TrustUnverifiedEmail bool     `json:"trustUnverifiedEmail"`
	ManualUserValidation bool     `json:"manualUserValidation"`
	RedirectURI          string   `json:"redirectUri"`
}

func adminConfigResponseFrom(view *AdminConfig) adminConfigResponse {
	response := adminConfigResponse{
		Issuer:               view.Issuer,
		ClientID:             view.ClientID,
		HasClientSecret:      view.HasClientSecret,
		ProviderName:         view.ProviderName,
		Scopes:               view.Scopes,
		Enabled:              view.Enabled,
		AllowedEmailDomains:  view.AllowedEmailDomains,
		AllowedGroups:        view.AllowedGroups,
		GroupsClaim:          view.GroupsClaim,
		TrustUnverifiedEmail: view.TrustUnverifiedEmail,
		ManualUserValidation: view.ManualUserValidation,
		RedirectURI:          view.RedirectURI,
	}
	// Encode empty lists as [], never null: the dashboard maps over them.
	if response.AllowedEmailDomains == nil {
		response.AllowedEmailDomains = []string{}
	}
	if response.AllowedGroups == nil {
		response.AllowedGroups = []string{}
	}
	return response
}

func (h *SSOHandler) GetConfigHandler(w http.ResponseWriter, r *http.Request) {
	view, err := h.service.GetAdminConfig(r.Context())
	if err != nil {
		renderSSOServiceError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, adminConfigResponseFrom(view))
}

func (h *SSOHandler) SaveConfigHandler(w http.ResponseWriter, r *http.Request) {
	var requestBody struct {
		Issuer               string   `json:"issuer"`
		ClientID             string   `json:"clientId"`
		ClientSecret         string   `json:"clientSecret"`
		ProviderName         string   `json:"providerName"`
		Scopes               string   `json:"scopes"`
		Enabled              bool     `json:"enabled"`
		AllowedEmailDomains  []string `json:"allowedEmailDomains"`
		AllowedGroups        []string `json:"allowedGroups"`
		GroupsClaim          string   `json:"groupsClaim"`
		TrustUnverifiedEmail bool     `json:"trustUnverifiedEmail"`
		ManualUserValidation bool     `json:"manualUserValidation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	view, err := h.service.SaveConfig(r.Context(), SaveConfigInput{
		Issuer:               requestBody.Issuer,
		ClientID:             requestBody.ClientID,
		ClientSecret:         requestBody.ClientSecret,
		ProviderName:         requestBody.ProviderName,
		Scopes:               requestBody.Scopes,
		Enabled:              requestBody.Enabled,
		AllowedEmailDomains:  requestBody.AllowedEmailDomains,
		AllowedGroups:        requestBody.AllowedGroups,
		GroupsClaim:          requestBody.GroupsClaim,
		TrustUnverifiedEmail: requestBody.TrustUnverifiedEmail,
		ManualUserValidation: requestBody.ManualUserValidation,
	})
	if err != nil {
		renderSSOServiceError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, adminConfigResponseFrom(view))
}

func (h *SSOHandler) DeleteConfigHandler(w http.ResponseWriter, r *http.Request) {
	if err := h.service.DeleteConfig(r.Context()); err != nil {
		renderSSOServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
