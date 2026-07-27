package infrastructure

import (
	"net/http"

	"github.com/gorilla/mux"
)

// How a dashboard session is obtained in the first place, called by the login
// page before anyone is signed in.
//
// AUTHENTICATION: none, by definition. These routes exist to produce the
// credential the /api surface then requires, so they cannot themselves sit
// behind it. /login trades an email and a password for a session, /refreshToken
// trades an expiring session for a fresh one, and the three SSO routes are the
// same exchange delegated to an identity provider: config feeds the login
// page's SSO button, login and callback are the two halves of the OIDC
// round-trip.
//
// The SSO routes are Enterprise (control-plane only) and are registered
// unconditionally, like the license routes. Without a database, without a
// configuration or without a valid license they answer accordingly, which
// keeps the routing table the same in every edition and puts the decision in
// one place, the handler.
func registerPreAuthRoutes(r *mux.Router, container *AppContainer) {
	corsSubrouter := r.PathPrefix("/auth").Subrouter()
	corsSubrouter.HandleFunc("/login", container.AuthHandler.LoginHandler).Methods(http.MethodPost)
	corsSubrouter.HandleFunc("/refreshToken", container.AuthHandler.RefreshTokenHandler).Methods(http.MethodPost)

	corsSubrouter.HandleFunc("/sso/config", container.SSOHandler.GetPublicConfigHandler).Methods(http.MethodGet)
	corsSubrouter.HandleFunc("/sso/login", container.SSOHandler.LoginRedirectHandler).Methods(http.MethodGet)
	corsSubrouter.HandleFunc("/sso/callback", container.SSOHandler.CallbackHandler).Methods(http.MethodGet)
}
