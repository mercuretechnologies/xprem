package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"xprem/internal/handlers"
	"xprem/internal/repository"
	"xprem/internal/services"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type AppIdentifiersHandler struct {
	identifierService *services.AppIdentifierService
}

func NewAppIdentifiersHandler(identifierService *services.AppIdentifierService) *AppIdentifiersHandler {
	return &AppIdentifiersHandler{
		identifierService: identifierService,
	}
}

func (h *AppIdentifiersHandler) GetAppIdentifiersHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	identifiers, err := h.identifierService.GetAppIdentifiers(r.Context(), appId)
	if err != nil {
		if errors.Is(err, repository.ErrNotSupportedInStatelessMode) {
			handlers.RenderError(w, http.StatusBadRequest, err.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while fetching app identifiers.")
		return
	}
	marshaledResponse, _ := json.Marshal(identifiers)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

func (h *AppIdentifiersHandler) CreateAppIdentifierHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	var requestBody struct {
		Platform   types.Platform `json:"platform"`
		Identifier string         `json:"identifier"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	identifierId, err := h.identifierService.CreateAppIdentifier(r.Context(), appId, requestBody.Platform, requestBody.Identifier)
	if err != nil {
		var valErr *validation.Error
		if errors.As(err, &valErr) {
			handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
			return
		}
		if alreadyExistsErr := (*repository.ErrResourceAlreadyExists)(nil); errors.As(err, &alreadyExistsErr) {
			handlers.RenderError(w, http.StatusConflict, alreadyExistsErr.Error())
			return
		}
		if errors.Is(err, repository.ErrNotSupportedInStatelessMode) {
			handlers.RenderError(w, http.StatusBadRequest, err.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while creating the app identifier.")
		return
	}
	marshaledResponse, _ := json.Marshal(map[string]any{
		"identifierId": identifierId,
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

func (h *AppIdentifiersHandler) SetBuildNumberHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	identifierId := vars["IDENTIFIER_ID"]
	if _, err := uuid.Parse(identifierId); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid identifier id")
		return
	}
	var requestBody struct {
		BuildNumber json.RawMessage `json:"buildNumber"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&requestBody); err != nil || len(requestBody.BuildNumber) == 0 {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body, expected {\"buildNumber\": <string or integer>}")
		return
	}
	var buildNumber string
	if err := json.Unmarshal(requestBody.BuildNumber, &buildNumber); err != nil {
		// Preserve legacy integer inputs without floating-point conversion. Fractions
		// must not accidentally become dotted iOS versions.
		buildNumber = string(requestBody.BuildNumber)
		for _, c := range buildNumber {
			if c < '0' || c > '9' {
				handlers.RenderError(w, http.StatusBadRequest, "buildNumber must be a string or a non-negative integer")
				return
			}
		}
	}
	err := h.identifierService.SetBuildNumber(r.Context(), appId, identifierId, buildNumber)
	if err != nil {
		var valErr *validation.Error
		if errors.As(err, &valErr) {
			handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
			return
		}
		if notFoundErr := (*repository.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			handlers.RenderError(w, http.StatusNotFound, notFoundErr.Error())
			return
		}
		if errors.Is(err, repository.ErrNotSupportedInStatelessMode) {
			handlers.RenderError(w, http.StatusBadRequest, err.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while setting the build number.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AppIdentifiersHandler) DeleteAppIdentifierHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	identifierId := vars["IDENTIFIER_ID"]
	if _, err := uuid.Parse(identifierId); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid identifier id")
		return
	}
	err := h.identifierService.DeleteAppIdentifier(r.Context(), appId, identifierId)
	if err != nil {
		if notFoundErr := (*repository.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			handlers.RenderError(w, http.StatusNotFound, notFoundErr.Error())
			return
		}
		if errors.Is(err, repository.ErrNotSupportedInStatelessMode) {
			handlers.RenderError(w, http.StatusBadRequest, err.Error())
			return
		}
		if restrictsErr := (*repository.ErrAppIdentifierRestrictsApiKeys)(nil); errors.As(err, &restrictsErr) {
			handlers.RenderError(w, http.StatusConflict, restrictsErr.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while deleting the app identifier.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
