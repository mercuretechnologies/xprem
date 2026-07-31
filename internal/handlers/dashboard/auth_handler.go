package handlers

import (
	"errors"
	"net/http"
	"xprem/internal/dashboard"
	"xprem/internal/handlers"
	"xprem/internal/helpers"
	"xprem/internal/ratelimit"
	"xprem/internal/services"
)

// maxCredentialsBody bounds the form these two routes read before the rate
// limiter gets a say.
const maxCredentialsBody = 32 * 1024

type AuthHandler struct {
	dashboardAuthService *services.DashboardAuthService
	limiter              *ratelimit.Limiter
}

func NewAuthHandler(dashboardAuthService *services.DashboardAuthService, limiter *ratelimit.Limiter) *AuthHandler {
	return &AuthHandler{
		dashboardAuthService: dashboardAuthService,
		limiter:              limiter,
	}
}

func (ah *AuthHandler) LoginHandler(w http.ResponseWriter, r *http.Request) {
	dashboardEnabled := dashboard.IsDashboardEnabled()
	if !dashboardEnabled {
		handlers.RenderError(w, http.StatusNotFound, "Dashboard is disabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCredentialsBody)
	email := r.FormValue("email")
	if email == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Email is empty")
		return
	}
	password := r.FormValue("password")
	if password == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Password is empty")
		return
	}
	// Before the password is verified, not after. Verifying it is deliberately
	// expensive, so an attempt that is going to be refused anyway must not be
	// allowed to spend that CPU: otherwise the limit removes the point of an
	// attack while leaving its cost.
	clientIP := helpers.ClientIP(r)
	if decision := ah.limiter.CheckLogin(email, clientIP); !decision.Allowed {
		handlers.RenderThrottled(w, decision.RetryAfter)
		return
	}
	session, err := ah.dashboardAuthService.LoginWithEmailPassword(r.Context(), email, password)
	if err != nil {
		// A missing ADMIN_EMAIL is the operator's misconfiguration, not the
		// user's bad credential, surface the instruction instead of a 401.
		if errors.Is(err, services.ErrAdminEmailNotSet) {
			handlers.RenderError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// A database outage is not a credentials problem either.
		if errors.Is(err, services.ErrAuthUnavailable) {
			handlers.RenderError(w, http.StatusInternalServerError, "Could not verify the credentials, try again later")
			return
		}
		// The password was correct but SSO is enforced for this account: the
		// actionable message sends the member to the SSO button.
		if errors.Is(err, services.ErrPasswordLoginDisabledBySSO) {
			handlers.RenderError(w, http.StatusForbidden, err.Error())
			return
		}
		// Credentials were valid but the account is not approved (or was
		// revoked). Distinct from invalid credentials so the person knows to
		// wait for an admin instead of retrying their password.
		if errors.Is(err, services.ErrAccountPendingApproval) {
			handlers.RenderError(w, http.StatusForbidden, err.Error())
			return
		}
		ah.limiter.RecordLoginFailure(email, clientIP)
		handlers.RenderError(w, http.StatusUnauthorized, "Invalid credentials")
		return
	}

	ah.limiter.RecordLoginSuccess(email)

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"token":"` + session.Token + `","refreshToken":"` + session.RefreshToken + `"}`))
	w.WriteHeader(http.StatusOK)
}

func (ah *AuthHandler) RefreshTokenHandler(w http.ResponseWriter, r *http.Request) {
	dashboardEnabled := dashboard.IsDashboardEnabled()
	if !dashboardEnabled {
		handlers.RenderError(w, http.StatusNotFound, "Dashboard is disabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCredentialsBody)
	refreshToken := r.FormValue("refreshToken")
	if refreshToken == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Refresh token is empty")
		return
	}
	// Nothing identifies the caller until the token is validated, so the
	// address is the only subject to count against here.
	clientIP := helpers.ClientIP(r)
	if decision := ah.limiter.CheckRefresh(clientIP); !decision.Allowed {
		handlers.RenderThrottled(w, decision.RetryAfter)
		return
	}
	session, err := ah.dashboardAuthService.RefreshSession(r.Context(), refreshToken)
	if err != nil {
		// A database outage must not read as an expired session, the client
		// would drop a perfectly valid refresh token and force a re-login.
		// It is not a rejected token either, so it is not counted.
		if errors.Is(err, services.ErrAuthUnavailable) {
			handlers.RenderError(w, http.StatusInternalServerError, "Error refreshing token")
			return
		}
		ah.limiter.RecordRefreshFailure(clientIP)
		handlers.RenderError(w, http.StatusUnauthorized, "Error refreshing token")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"token":"` + session.Token + `","refreshToken":"` + session.RefreshToken + `"}`))
	w.WriteHeader(http.StatusOK)
}
