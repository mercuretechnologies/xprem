package infrastructure

import (
	"net/http"

	"github.com/gorilla/mux"
)

func registerMCPRoutes(r *mux.Router, container *AppContainer) {
	if container.MCPHandler == nil {
		return
	}
	mcpRouter := r.PathPrefix("/mcp").Subrouter()
	mcpRouter.HandleFunc("", container.MCPHandler.GlobalHandler).Methods(http.MethodPost)
}
