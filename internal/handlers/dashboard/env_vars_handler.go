package handlers

import (
	"encoding/json"
	"net/http"
	"xprem/internal/handlers"
	"xprem/internal/services"

	"github.com/gorilla/mux"
)

type EnvVarsHandler struct {
	envVarService *services.EnvVarService
}

func NewEnvVarsHandler(envVarService *services.EnvVarService) *EnvVarsHandler {
	return &EnvVarsHandler{
		envVarService: envVarService,
	}
}

func (h *EnvVarsHandler) ListEnvVarsHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	envVars, err := h.envVarService.ListEnvVars(r.Context(), appId)
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while listing env vars.")
		return
	}
	marshaledResponse, _ := json.Marshal(envVars)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

func (h *EnvVarsHandler) SetEnvVarHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branch := vars["BRANCH"]
	key := vars["KEY"]
	var requestBody struct {
		Value    *string `json:"value"`
		IsPublic bool    `json:"isPublic"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&requestBody); err != nil || requestBody.Value == nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body, expected {\"value\": <string>, \"isPublic\": <bool>}")
		return
	}
	err := h.envVarService.SetEnvVar(r.Context(), appId, branch, key, *requestBody.Value, requestBody.IsPublic)
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while saving the env var.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *EnvVarsHandler) RevealEnvVarHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	value, err := h.envVarService.RevealEnvVar(r.Context(), vars["APP_ID"], vars["BRANCH"], vars["KEY"])
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while reading the env var.")
		return
	}
	marshaledResponse, _ := json.Marshal(map[string]string{"value": value})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

func (h *EnvVarsHandler) DeleteEnvVarHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	err := h.envVarService.DeleteEnvVar(r.Context(), vars["APP_ID"], vars["BRANCH"], vars["KEY"])
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while deleting the env var.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
