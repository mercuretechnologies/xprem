package handlers

import (
	"errors"
	"log"
	"net/http"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/validation"

	"github.com/gorilla/mux"
)

type BundlePatchHandler struct {
	bsdiff *services.BsDiffService
}

func NewBundlePatchHandler(bsdiff *services.BsDiffService) *BundlePatchHandler {
	return &BundlePatchHandler{bsdiff: bsdiff}
}

func (h *BundlePatchHandler) GetUpdatePatchesHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	patches, err := h.bsdiff.ListPatches(r.Context(), vars["APP_ID"], vars["BRANCH"], vars["UPDATE_ID"])
	if err != nil {
		renderBundlePatchError(w, err, "An internal error occurred while listing the bundle patches.")
		return
	}
	handlers.RenderJSON(w, http.StatusOK, patches)
}

type recomputePatchesResponse struct {
	Scheduled int `json:"scheduled"`
}

func (h *BundlePatchHandler) RecomputeUpdatePatchesHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	scheduled, err := h.bsdiff.RecomputePatches(r.Context(), vars["APP_ID"], vars["BRANCH"], vars["RUNTIME_VERSION"], vars["UPDATE_ID"])
	if err != nil {
		renderBundlePatchError(w, err, "An internal error occurred while scheduling the bundle patches.")
		return
	}
	handlers.RenderJSON(w, http.StatusOK, recomputePatchesResponse{Scheduled: scheduled})
}

func renderBundlePatchError(w http.ResponseWriter, err error, fallbackDetail string) {
	var valErr *validation.Error
	switch {
	case errors.Is(err, services.ErrBundleDiffingUnavailable):
		handlers.RenderError(w, http.StatusBadRequest, "Bundle diffing is disabled on this server or needs the database control plane.")
	case errors.Is(err, services.ErrUpdateHasNoBundle):
		handlers.RenderError(w, http.StatusBadRequest, "A rollback has no bundle, so it has no patches.")
	case errors.As(err, &valErr):
		handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
	default:
		log.Printf("[bsdiff] %s: %v", fallbackDetail, err)
		handlers.RenderError(w, http.StatusInternalServerError, fallbackDetail)
	}
}
