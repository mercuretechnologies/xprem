package middleware

import (
	"errors"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"
	"net/http"

	"github.com/gorilla/mux"
)

// The principal and CLI-credential context helpers live in services
// (request_context.go), next to the types and producers they belong to; this
// package only stamps and reads them through the services accessors.

// NewAuthMiddleware guards a route with one of two unrelated credentials,
// picked by the Use-Cli-Auth header:
//   - "true": a CLI credential scoped to an app (an eoo_ API key in DB mode, an
//     Expo token/session in stateless mode) -> cliAuthService.
//   - otherwise: the dashboard's own session JWT -> dashboardAuthService. The
//     resolved principal is stored on the request context for downstream
//     handlers and the admin gate.
//
// Both travel as `Authorization: Bearer …`, which is why the header decides
// which one to expect rather than the credential's shape.
func NewAuthMiddleware(dashboardAuthService *services.DashboardAuthService, cliAuthService *services.CliAuthService) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			useCliAuth := r.Header.Get("Use-Cli-Auth")
			if useCliAuth == "true" {
				// CLI-driven external authentication requires an APP_ID path variable
				// to locate the correct tenant boundary.
				// - In DB Mode: Used to check the api_keys table for app-scoped access.
				// - In Stateless Mode: Relayed to select the correct EXPO_ACCESS_TOKEN.
				// On global or app-agnostic routes (like /api/settings or /api/apps),
				// there is no app context anchor, making Use-Cli-Auth invalid.
				appId := mux.Vars(r)["APP_ID"]
				if appId == "" {
					http.Error(w, "Use-Cli-Auth requires an app-scoped route", http.StatusUnauthorized)
					return
				}

				auth := helpers.GetAuth(r)
				// Authentication only. What this credential may DO is the
				// route's question, and it is answered where the route is
				// declared (internal/router/access.go), which is the one place
				// holding the branch and the action together. This used to
				// happen here, on a branch name read out of mux.Vars, which
				// made the SPELLING of a path variable load-bearing: a
				// token-reachable route spelled {BRANCH_ID} would have passed
				// an empty name and skipped the check.
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
			// Not just "is this token authentic": the account behind it is
			// re-read here, so a session dies the moment the account is
			// deleted, disabled or revoked, instead of surviving up to the
			// token's two hours.
			principal, err := dashboardAuthService.AuthenticateSession(r.Context(), bearerToken)
			if err != nil {
				// A database outage must not read as a dead session, or a blip
				// would sign every account out at once.
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

// NewDashboardOnlyMiddleware refuses a CLI credential on a group of routes,
// whatever the routes are. It runs after NewAuthMiddleware, which is what put
// the credential on the context, and it turns "this group takes accounts and
// nothing else" into something the group carries rather than something a
// reader has to derive.
//
// It exists because the alternative was an invariant nobody checks. A CLI
// credential is app-scoped, so NewAuthMiddleware already refuses it on any
// route without an {APP_ID} path variable, which today happens to be every
// route in routes_account.go. That is a property of the paths, not a decision:
// the first account route to name an app would silently become CLI-reachable.
// This says the decision out loud, so adding such a route stays safe.
func NewDashboardOnlyMiddleware() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if services.CliAuthFromContext(r.Context()) != nil {
				http.Error(w, "This route requires a dashboard session", http.StatusForbidden)
				return
			}
			// No principal either means the group was wired without an
			// authentication middleware in front of it. Refusing is the only
			// safe reading: the routes below expect a signed-in account.
			if services.PrincipalFromContext(r.Context()) == nil {
				http.Error(w, "This route requires a dashboard session", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// NewAdminMiddleware guards a route behind the account-level admin flag. It
// only accepts dashboard sessions — a CLI credential is app-scoped publishing
// access, not an account, so it never reaches admin-gated routes.
//
// The flag is re-read from the users table here even though the principal now
// carries a fresh one: NewAuthMiddleware resolves the account on every request,
// so this read agrees with it by construction. It is kept because this gate is
// the last thing between a member and the administration surface, and a gate
// that reads its own decision's input is one that cannot be defeated by a
// future change to how principals are built. userRepo is nil in stateless mode,
// where the single ADMIN_EMAIL account is always an admin and the claim alone
// is authoritative.
func NewAdminMiddleware(userRepo services.UserRepository) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := services.PrincipalFromContext(r.Context())
			if principal == nil {
				http.Error(w, "This action requires a dashboard session", http.StatusForbidden)
				return
			}
			if userRepo != nil {
				user, err := userRepo.GetUserByID(r.Context(), principal.UserId)
				if err != nil {
					// Only a missing row means the account is gone; an
					// infrastructure failure must not read as a dead session.
					if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
						http.Error(w, "Invalid token", http.StatusUnauthorized)
					} else {
						http.Error(w, "Could not verify the account", http.StatusInternalServerError)
					}
					return
				}
				if !user.IsAdmin {
					http.Error(w, "This action requires an admin account", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
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
