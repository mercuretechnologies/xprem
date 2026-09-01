package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"xprem/config"
	cache2 "xprem/internal/cache"
	"xprem/internal/dashboard"
	"xprem/internal/handlers"
	"xprem/internal/helpers"
	"xprem/internal/providers/expo"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/validation"

	"github.com/gorilla/mux"
)

type ExpoImportHandler struct {
	importService *services.ExpoImportService
}

func NewExpoImportHandler(importService *services.ExpoImportService) *ExpoImportHandler {
	return &ExpoImportHandler{importService: importService}
}

// Caller mistakes are 4xx, Expo being unreachable is 502.
func renderExpoImportError(w http.ResponseWriter, err error) {
	var valErr *validation.Error
	if errors.As(err, &valErr) {
		handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
		return
	}
	var expoErr *expo.APIError
	if errors.As(err, &expoErr) {
		handlers.RenderError(w, expoErr.StatusHint, expoErr.Message)
		return
	}
	if alreadyExistsErr := (*store.ErrResourceAlreadyExists)(nil); errors.As(err, &alreadyExistsErr) {
		handlers.RenderError(w, http.StatusConflict, alreadyExistsErr.Error())
		return
	}
	if errors.Is(err, store.ErrNotSupportedInStatelessMode) {
		handlers.RenderError(w, http.StatusBadRequest, "Importing apps requires the control plane (set DB_URL).")
		return
	}
	if errors.Is(err, services.ErrHistoryImportAlreadyRunning) {
		handlers.RenderError(w, http.StatusConflict, err.Error())
		return
	}
	handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while importing the app.")
}

func (h *ExpoImportHandler) ListExpoAppsHandler(w http.ResponseWriter, r *http.Request) {
	accounts, err := h.importService.ListImportableApps(r.Context(), helpers.GetExpoAuth(r))
	if err != nil {
		renderExpoImportError(w, err)
		return
	}
	marshaledResponse, _ := json.Marshal(accounts)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

func (h *ExpoImportHandler) PreviewExpoImportHandler(w http.ResponseWriter, r *http.Request) {
	plan, err := h.importService.PreviewImport(r.Context(), helpers.GetExpoAuth(r), r.URL.Query().Get("expoAppId"))
	if err != nil {
		renderExpoImportError(w, err)
		return
	}
	marshaledResponse, _ := json.Marshal(plan)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

func (h *ExpoImportHandler) ImportExpoAppHandler(w http.ResponseWriter, r *http.Request) {
	var requestBody struct {
		ExpoAppId  string            `json:"expoAppId"`
		KeysConfig config.KeysConfig `json:"keysConfig"`
		// Newest EAS update groups to also copy in a background job; 0 for none.
		HistoryLimit int `json:"historyLimit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	result, err := h.importService.ImportApp(r.Context(), helpers.GetExpoAuth(r), requestBody.ExpoAppId, requestBody.KeysConfig, requestBody.HistoryLimit)
	if err != nil {
		renderExpoImportError(w, err)
		return
	}
	marshaledResponse, _ := json.Marshal(result)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(marshaledResponse)

	cache2.GetCache().Delete(dashboard.ComputeGetAppsCacheKey())
}

func (h *ExpoImportHandler) GetExpoImportJobHandler(w http.ResponseWriter, r *http.Request) {
	jobId := mux.Vars(r)["JOB_ID"]
	status, ok := h.importService.GetHistoryJobStatus(r.Context(), jobId)
	if !ok {
		handlers.RenderError(w, http.StatusNotFound, "Unknown or expired import job.")
		return
	}
	marshaledResponse, _ := json.Marshal(status)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

// A null jobId means none; 200 rather than 404 keeps the routine dashboard
// poll out of the browser's error console.
func (h *ExpoImportHandler) GetExpoImportAppJobHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	response := struct {
		JobId  *string                        `json:"jobId"`
		Status *services.ExpoHistoryJobStatus `json:"status"`
	}{}
	if jobId, status, ok := h.importService.GetAppHistoryJob(r.Context(), appId); ok {
		response.JobId = &jobId
		response.Status = status
	}
	marshaledResponse, _ := json.Marshal(response)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

func (h *ExpoImportHandler) CancelExpoImportJobHandler(w http.ResponseWriter, r *http.Request) {
	jobId := mux.Vars(r)["JOB_ID"]
	if err := h.importService.CancelHistoryJob(r.Context(), jobId); err != nil {
		if errors.Is(err, services.ErrHistoryJobNotFound) {
			handlers.RenderError(w, http.StatusNotFound, "Unknown or expired import job.")
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "Could not cancel the import job.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
