package infrastructure

import (
	"net/http"

	"github.com/gorilla/mux"
)

func registerMCPRoutes(r *mux.Router, container *AppContainer) {
	mcpRouter := r.PathPrefix("/mcp").Subrouter()
	mcpRouter.HandleFunc("", container.MCPHandler.GlobalHandler).Methods(http.MethodPost)
}
