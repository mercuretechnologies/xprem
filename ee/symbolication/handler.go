// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"errors"
	"log"
	"net/http"
	"xprem/internal/handlers"
	"xprem/internal/jobs"
	"xprem/internal/validation"

	"github.com/gorilla/mux"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) GetUpdateSourcemapHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sourcemap, err := h.service.GetUpdateSourcemap(r.Context(), vars["APP_ID"], vars["BRANCH"], vars["RUNTIME_VERSION"], vars["UPDATE_ID"])
	if err != nil {
		renderError(w, err, "An internal error occurred while reading the source map index.")
		return
	}
	handlers.RenderJSON(w, http.StatusOK, sourcemap)
}

type reindexResponse struct {
	Scheduled bool `json:"scheduled"`
}

func (h *Handler) ReindexUpdateSourcemapHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	if err := h.service.Reindex(r.Context(), vars["APP_ID"], vars["BRANCH"], vars["RUNTIME_VERSION"], vars["UPDATE_ID"]); err != nil {
		renderError(w, err, "An internal error occurred while scheduling the source map index.")
		return
	}
	handlers.RenderJSON(w, http.StatusOK, reindexResponse{Scheduled: true})
}

func renderError(w http.ResponseWriter, err error, fallbackDetail string) {
	var valErr *validation.Error
	switch {
	case errors.Is(err, ErrUnavailable):
		handlers.RenderError(w, http.StatusBadRequest, "Source map indexing needs UPLOAD_SOURCEMAPS, the database control plane and an enterprise license.")
	case errors.Is(err, ErrNoSourcemap):
		handlers.RenderError(w, http.StatusNotFound, "This update was published without a source map.")
	case errors.Is(err, ErrUpdateNotFound):
		handlers.RenderError(w, http.StatusNotFound, "Update not found.")
	case errors.Is(err, jobs.ErrAlreadyRunning):
		handlers.RenderError(w, http.StatusConflict, "An index job for this update is already running.")
	case errors.As(err, &valErr):
		handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
	default:
		log.Printf("[sourcemap] %s: %v", fallbackDetail, err)
		handlers.RenderError(w, http.StatusInternalServerError, fallbackDetail)
	}
}
