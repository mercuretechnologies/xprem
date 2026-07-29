// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package branchprotection

import (
	"encoding/json"
	"errors"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/dashboard"
	"expo-open-ota/internal/handlers"
	"net/http"

	"github.com/gorilla/mux"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) SetBranchProtectionHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	if branchName == "" {
		handlers.RenderError(w, http.StatusBadRequest, "No branch provided")
		return
	}
	var req struct {
		Protected bool `json:"protected"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if err := h.service.SetBranchProtection(r.Context(), appId, branchName, req.Protected); err != nil {
		switch {
		case errors.Is(err, ErrRequiresControlPlane):
			handlers.RenderError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrRequiresValidLicense):
			handlers.RenderError(w, http.StatusForbidden, err.Error())
		case errors.Is(err, ErrBranchNotFound):
			handlers.RenderError(w, http.StatusNotFound, err.Error())
		default:
			handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)

	// The dashboard's branch listing carries the protected flag, so its cached
	// copy is stale now.
	cache.GetCache().Delete(dashboard.ComputeGetBranchesCacheKey(appId))
}
