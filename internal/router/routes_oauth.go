package infrastructure

import (
	"expo-open-ota/internal/middleware"
	"expo-open-ota/internal/oauth"
	"net/http"

	"github.com/gorilla/mux"
)

// registerOAuthRoutes registers the OAuth 2.1 surface MCP clients authenticate
// through: the discovery documents and dynamic client registration. Absent in
// stateless mode, which has nowhere to store clients.
func registerOAuthRoutes(r *mux.Router, container *AppContainer) {
	if container.OAuthHandler == nil {
		return
	}
	h := container.OAuthHandler

	// Path-inserted form first (RFC 9728 for a resource at /mcp), root form as
	// the fallback non-conforming clients probe.
	r.HandleFunc("/.well-known/oauth-protected-resource/mcp", oauth.WithCORS(h.ProtectedResourceMetadataHandler)).Methods(http.MethodGet, http.MethodOptions)
	r.HandleFunc("/.well-known/oauth-protected-resource", oauth.WithCORS(h.ProtectedResourceMetadataHandler)).Methods(http.MethodGet, http.MethodOptions)
	r.HandleFunc("/.well-known/oauth-authorization-server", oauth.WithCORS(h.AuthorizationServerMetadataHandler)).Methods(http.MethodGet, http.MethodOptions)

	r.HandleFunc("/oauth/register", oauth.WithCORS(h.RegisterHandler)).Methods(http.MethodPost, http.MethodOptions)
	// Top-level browser navigation, no CORS involved.
	r.HandleFunc("/oauth/authorize", h.AuthorizeHandler).Methods(http.MethodGet)
}

// registerOAuthApiRoutes mounts the consent endpoint behind the dashboard
// session; api is the authenticated /api subrouter.
func registerOAuthApiRoutes(api *mux.Router, container *AppContainer) {
	if container.OAuthHandler == nil {
		return
	}
	consentRouter := api.PathPrefix("/oauth").Subrouter()
	consentRouter.Use(middleware.NewDashboardOnlyMiddleware())
	consentRouter.HandleFunc("/consent", container.OAuthHandler.ConsentHandler).Methods(http.MethodPost)
}
