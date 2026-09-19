package handlers

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"xprem/internal/bucket"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/gorilla/mux"
)

type BuildCacheHandler struct{ service *services.BuildCacheService }

func NewBuildCacheHandler(service *services.BuildCacheService) *BuildCacheHandler {
	return &BuildCacheHandler{service: service}
}

func renderBuildCacheError(w http.ResponseWriter, err error) {
	w.Header().Set("Cache-Control", "no-store")
	var missing *store.ErrResourceNotFound
	switch {
	case errors.As(err, &missing):
		RenderError(w, http.StatusNotFound, "Cache object not found.")
	case errors.Is(err, bucket.ErrCacheObjectExists), errors.Is(err, services.ErrBuildCachePending):
		RenderError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrBuildCacheFull):
		RenderError(w, http.StatusRequestEntityTooLarge, err.Error())
	case validation.IsValidationError(err), errors.Is(err, services.ErrBuildCacheIntegrity), errors.Is(err, services.ErrBuildCacheArchive), errors.Is(err, bucket.ErrCacheDirectUploadRequired):
		RenderError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotSupportedInStatelessMode):
		RenderError(w, http.StatusBadRequest, err.Error())
	default:
		RenderError(w, http.StatusInternalServerError, "Could not process the cache request.")
	}
}

func (h *BuildCacheHandler) Reserve(w http.ResponseWriter, r *http.Request) {
	var input services.BuildCacheInput
	if !decodeBuildBody(w, r, &input) {
		return
	}
	result, err := h.service.Reserve(r.Context(), mux.Vars(r)["APP_ID"], services.BuildIdentifierFromContext(r.Context()), input)
	if err != nil {
		renderBuildCacheError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	RenderJSON(w, http.StatusCreated, result)
}

func (h *BuildCacheHandler) Complete(w http.ResponseWriter, r *http.Request) {
	object, err := h.service.Complete(r.Context(), mux.Vars(r)["APP_ID"], services.BuildIdentifierFromContext(r.Context()), mux.Vars(r)["UPLOAD_ID"])
	if err != nil {
		renderBuildCacheError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	RenderJSON(w, http.StatusOK, object)
}

func (h *BuildCacheHandler) UploadLocal(w http.ResponseWriter, r *http.Request) {
	err := h.service.UploadLocal(r.Context(), mux.Vars(r)["APP_ID"], services.BuildIdentifierFromContext(r.Context()), mux.Vars(r)["UPLOAD_ID"], http.MaxBytesReader(w, r.Body, types.MaxBuildCacheObjectBytes+1))
	if err != nil {
		renderBuildCacheError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *BuildCacheHandler) Find(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	object, err := h.service.Find(r.Context(), vars["APP_ID"], services.BuildIdentifierFromContext(r.Context()), types.BuildCacheNamespace(vars["NAMESPACE"]), vars["CACHE_KEY"])
	if err != nil {
		renderBuildCacheError(w, err)
		return
	}
	url, err := h.service.DownloadURL(r.Context(), *object)
	if err != nil {
		renderBuildCacheError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	RenderJSON(w, http.StatusOK, struct {
		Object *types.BuildCacheObject `json:"object"`
		URL    string                  `json:"url"`
	}{object, url})
}

func (h *BuildCacheHandler) Download(w http.ResponseWriter, r *http.Request) {
	object, err := h.service.Get(r.Context(), mux.Vars(r)["APP_ID"], services.BuildIdentifierFromContext(r.Context()), mux.Vars(r)["UPLOAD_ID"])
	if err != nil {
		renderBuildCacheError(w, err)
		return
	}
	file, err := h.service.Download(r.Context(), *object)
	if err != nil {
		renderBuildCacheError(w, err)
		return
	}
	if file == nil {
		http.NotFound(w, r)
		return
	}
	defer file.Reader.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(object.Size, 10))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, file.Reader)
}
