package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/store"

	"github.com/gorilla/mux"
)

type EnvironmentsHandler struct {
	environmentService *services.EnvironmentService
}

func NewEnvironmentsHandler(environmentService *services.EnvironmentService) *EnvironmentsHandler {
	return &EnvironmentsHandler{
		environmentService: environmentService,
	}
}

func (h *EnvironmentsHandler) ListEnvironmentsHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	environments, err := h.environmentService.ListEnvironments(r.Context(), appId)
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while listing environments.")
		return
	}
	marshaledResponse, _ := json.Marshal(environments)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

func (h *EnvironmentsHandler) CreateEnvironmentHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	var requestBody struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&requestBody); err != nil || requestBody.Name == "" {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body, expected {\"name\": <string>}")
		return
	}
	id, err := h.environmentService.CreateEnvironment(r.Context(), appId, requestBody.Name)
	if err != nil {
		if alreadyExistsErr := (*store.ErrResourceAlreadyExists)(nil); errors.As(err, &alreadyExistsErr) {
			handlers.RenderError(w, http.StatusConflict, alreadyExistsErr.Error())
			return
		}
		renderServiceError(w, err, "An internal error occurred while creating the environment.")
		return
	}
	marshaledResponse, _ := json.Marshal(map[string]string{"id": id, "name": requestBody.Name})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(marshaledResponse)
}

func (h *EnvironmentsHandler) DeleteEnvironmentHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	err := h.environmentService.DeleteEnvironment(r.Context(), vars["APP_ID"], vars["ENVIRONMENT"])
	if err != nil {
		if inUseErr := (*store.ErrEnvironmentHasChannels)(nil); errors.As(err, &inUseErr) {
			handlers.RenderError(w, http.StatusConflict, inUseErr.Error())
			return
		}
		renderServiceError(w, err, "An internal error occurred while deleting the environment.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *EnvironmentsHandler) SetEnvVarHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var requestBody struct {
		Value    *string `json:"value"`
		IsPublic bool    `json:"isPublic"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&requestBody); err != nil || requestBody.Value == nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body, expected {\"value\": <string>, \"isPublic\": <bool>}")
		return
	}
	err := h.environmentService.SetEnvVar(r.Context(), vars["APP_ID"], vars["ENVIRONMENT"], vars["KEY"], *requestBody.Value, requestBody.IsPublic)
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while saving the env var.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *EnvironmentsHandler) RevealEnvVarHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	value, err := h.environmentService.RevealEnvVar(r.Context(), vars["APP_ID"], vars["ENVIRONMENT"], vars["KEY"])
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while reading the env var.")
		return
	}
	marshaledResponse, _ := json.Marshal(map[string]string{"value": value})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

func (h *EnvironmentsHandler) DeleteEnvVarHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	err := h.environmentService.DeleteEnvVar(r.Context(), vars["APP_ID"], vars["ENVIRONMENT"], vars["KEY"])
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while deleting the env var.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetChannelEnvironmentHandler binds a channel to an environment; a null
// environment unbinds it.
func (h *EnvironmentsHandler) SetChannelEnvironmentHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	// RawMessage so a body missing the key is refused rather than read as an
	// explicit null, which would unbind the channel.
	var requestBody struct {
		Environment json.RawMessage `json:"environment"`
	}
	var environment *string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&requestBody); err != nil ||
		len(requestBody.Environment) == 0 || json.Unmarshal(requestBody.Environment, &environment) != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body, expected {\"environment\": <string|null>}")
		return
	}
	err := h.environmentService.SetChannelEnvironment(r.Context(), vars["APP_ID"], vars["CHANNEL"], environment)
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while updating the channel environment.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
