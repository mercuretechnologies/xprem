package infrastructure

import (
	"expo-open-ota/internal/middleware"
	"net/http"

	"github.com/gorilla/mux"
)

func registerMCPRoutes(r *mux.Router, container *AppContainer) {
	if container.MCPHandler == nil {
		return
	}
	mcpRouter := r.PathPrefix("/mcp").Subrouter()
	mcpRouter.Use(middleware.NewOAuthMiddleware(container.OAuthService))
	// POST carries the JSON-RPC messages, GET the SSE notification stream,
	// DELETE the session teardown; all three are the same streamable endpoint.
	mcpRouter.HandleFunc("", container.MCPHandler.GlobalHandler).Methods(http.MethodPost, http.MethodGet, http.MethodDelete)
}
