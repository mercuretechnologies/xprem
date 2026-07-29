package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	cache2 "xprem/internal/cache"
	"xprem/internal/dashboard"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/validation"

	"github.com/gorilla/mux"
)

type ApiKeyHandler struct {
	cliAuthService *services.CliAuthService
}

func NewApiKeyHandler(cliAuthService *services.CliAuthService) *ApiKeyHandler {
	return &ApiKeyHandler{
		cliAuthService: cliAuthService,
	}
}

func (h *ApiKeyHandler) CreateApiKeyHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.Name == "" {
		handlers.RenderError(w, http.StatusBadRequest, "API key name is empty")
		return
	}
	apiKey, err := h.cliAuthService.GenerateAPIKey(r.Context(), appId, req.Name)
	if err != nil {
		var valErr *validation.Error
		if errors.As(err, &valErr) {
			handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while generating the API key.")
		return
	}
	marshaledResponse, _ := json.Marshal(map[string]interface{}{
		"apiKey": apiKey,
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(marshaledResponse)

	cache := cache2.GetCache()
	cacheKey := dashboard.ComputeGetApiKeysCacheKey(appId)
	cache.Delete(cacheKey)
}

func (h *ApiKeyHandler) GetApiKeysHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	cache := cache2.GetCache()
	cacheKey := dashboard.ComputeGetApiKeysCacheKey(appId)
	if cacheValue := cache.Get(cacheKey); cacheValue != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(cacheValue))
		return
	}
	apiKeysMetadata, err := h.cliAuthService.GetApiKeysMetadata(r.Context(), appId)
	if err != nil {
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while fetching API keys metadata.")
		return
	}
	marshaledResponse, _ := json.Marshal(apiKeysMetadata)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)

	ttl := 60
	cache.Set(cacheKey, string(marshaledResponse), &ttl)
}

func (h *ApiKeyHandler) RevokeApiKeyHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	apiKeyId := vars["API_KEY_ID"]
	if apiKeyId == "" {
		handlers.RenderError(w, http.StatusBadRequest, "API key ID is empty")
		return
	}
	err := h.cliAuthService.RevokeApiKey(r.Context(), appId, apiKeyId)
	if err != nil {
		var valErr *validation.Error
		if errors.As(err, &valErr) {
			handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
			return
		}
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			handlers.RenderError(w, http.StatusNotFound, notFoundErr.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while revoking the API key.")
		return
	}
	w.WriteHeader(http.StatusNoContent)

	cache := cache2.GetCache()
	cacheKey := dashboard.ComputeGetApiKeysCacheKey(appId)
	cache.Delete(cacheKey)
}
