package infrastructure

import (
	"net/http"

	"github.com/gorilla/mux"
)

// registerPreAuthRoutes registers how a dashboard session is obtained before
// anyone is signed in: password login, refresh, and the SSO login/callback
// exchange.
func registerPreAuthRoutes(r *mux.Router, container *AppContainer) {
	corsSubrouter := r.PathPrefix("/auth").Subrouter()
	corsSubrouter.HandleFunc("/login", container.AuthHandler.LoginHandler).Methods(http.MethodPost)
	corsSubrouter.HandleFunc("/refreshToken", container.AuthHandler.RefreshTokenHandler).Methods(http.MethodPost)

	corsSubrouter.HandleFunc("/sso/config", container.SSOHandler.GetPublicConfigHandler).Methods(http.MethodGet)
	corsSubrouter.HandleFunc("/sso/login", container.SSOHandler.LoginRedirectHandler).Methods(http.MethodGet)
	corsSubrouter.HandleFunc("/sso/callback", container.SSOHandler.CallbackHandler).Methods(http.MethodGet)
}
