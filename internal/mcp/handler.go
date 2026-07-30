package mcp

import (
	"net/http"
)

type MCPHandler struct {
	service *MCPService
}

func NewMCPHandler(service *MCPService) *MCPHandler {
	return &MCPHandler{service: service}
}

func (h *MCPHandler) GlobalHandler(w http.ResponseWriter, r *http.Request) {
	h.service.streamable.ServeHTTP(w, r)
}
