package infrastructure

import (
	"expo-open-ota/internal/middleware"
	"net/http"

	"github.com/gorilla/mux"
)

// mcpCORS opens the MCP endpoint to browser-based clients (MCP Inspector,
// web agents). It must run BEFORE the auth middleware: a preflight carries no
// Authorization header, so it has to be answered here, and the headers must
// also land on 401s or the browser hides the WWW-Authenticate challenge the
// whole discovery flow starts from.
func mcpCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, mcp-protocol-version, Last-Event-ID")
		w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id, WWW-Authenticate")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func registerMCPRoutes(r *mux.Router, container *AppContainer) {
	if container.MCPHandler == nil {
		return
	}
	mcpRouter := r.PathPrefix("/mcp").Subrouter()
	mcpRouter.Use(mcpCORS)
	mcpRouter.Use(middleware.NewOAuthMiddleware(container.OAuthService))
	// POST carries the JSON-RPC messages, GET the SSE notification stream,
	// DELETE the session teardown; all three are the same streamable endpoint.
	mcpRouter.HandleFunc("", container.MCPHandler.GlobalHandler).
		Methods(http.MethodPost, http.MethodGet, http.MethodDelete, http.MethodOptions)
}
