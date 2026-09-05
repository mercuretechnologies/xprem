package middleware

import (
	"errors"
	"net/http"
	"xprem/internal/handlers"
	"xprem/internal/helpers"
	"xprem/internal/services"

	"github.com/gorilla/mux"
)

// NewAuthMiddleware guards a route with one of two unrelated credentials,
// picked by the Use-Cli-Auth header: a CLI credential scoped to an app, or the
// dashboard's own session JWT. Both travel as `Authorization: Bearer …`.
func NewAuthMiddleware(dashboardAuthService *services.DashboardAuthService, cliAuthService *services.CliAuthService) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			useCliAuth := r.Header.Get("Use-Cli-Auth")
			if useCliAuth == "true" {
				appId := mux.Vars(r)["APP_ID"]
				if appId == "" {
					http.Error(w, "Use-Cli-Auth requires an app-scoped route", http.StatusUnauthorized)
					return
				}

				auth := helpers.GetAuth(r)
				credential, err := cliAuthService.AuthenticateCliCredential(r.Context(), appId, auth)
				if err != nil {
					handlers.RenderCliAuthError(w, err)
					return
				}

				next.ServeHTTP(w, r.WithContext(services.WithCliAuth(r.Context(), credential)))
				return
			}
			bearerToken, err := helpers.GetBearerToken(r)
			if err != nil {
				http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
				return
			}
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, "No Authorization header provided", http.StatusUnauthorized)
				return
			}
			principal, err := dashboardAuthService.AuthenticateSession(r.Context(), bearerToken)
			if err != nil {
				// A database outage must not read as a dead session.
				if errors.Is(err, services.ErrAuthUnavailable) {
					http.Error(w, "Could not verify the account", http.StatusInternalServerError)
					return
				}
				http.Error(w, "Invalid token", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(services.WithPrincipal(r.Context(), principal)))
		})
	}
}

// NewDashboardOnlyMiddleware refuses a CLI credential, and refuses a missing
// principal, on the routes it wraps.
func NewDashboardOnlyMiddleware() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if services.CliAuthFromContext(r.Context()) != nil {
				http.Error(w, "This route requires a dashboard session", http.StatusForbidden)
				return
			}
			if services.PrincipalFromContext(r.Context()) == nil {
				http.Error(w, "This route requires a dashboard session", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// NewAdminMiddleware guards a route behind the account-level admin flag.
// NewAdminMiddleware gates a route behind the principal's admin flag, which the
// auth middleware reads from the users table on every request.
func NewAdminMiddleware() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := services.PrincipalFromContext(r.Context())
			if principal == nil {
				http.Error(w, "This action requires a dashboard session", http.StatusForbidden)
				return
			}
			if !principal.IsAdmin {
				http.Error(w, "This action requires an admin account", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
