package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/validation"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type CredentialsHandler struct {
	credentialsService *services.CredentialsService
}

func NewCredentialsHandler(credentialsService *services.CredentialsService) *CredentialsHandler {
	return &CredentialsHandler{
		credentialsService: credentialsService,
	}
}

// credentialsVars extracts and validates the route vars shared by the three
// handlers; a "" identifier id means the response was already written.
func credentialsVars(w http.ResponseWriter, r *http.Request) (string, string) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	identifierId := vars["IDENTIFIER_ID"]
	if _, err := uuid.Parse(identifierId); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid identifier id")
		return "", ""
	}
	return appId, identifierId
}

func renderCredentialsError(w http.ResponseWriter, err error, fallback string) {
	var valErr *validation.Error
	if errors.As(err, &valErr) {
		handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
		return
	}
	if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
		handlers.RenderError(w, http.StatusNotFound, notFoundErr.Error())
		return
	}
	if errors.Is(err, store.ErrNotSupportedInStatelessMode) {
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
		return
	}
	handlers.RenderError(w, http.StatusInternalServerError, fallback)
}

func (h *CredentialsHandler) GetAndroidCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	appId, identifierId := credentialsVars(w, r)
	if identifierId == "" {
		return
	}
	metadata, err := h.credentialsService.GetAndroidCredentialsMetadata(r.Context(), appId, identifierId)
	if err != nil {
		renderCredentialsError(w, err, "An internal error occurred while fetching android credentials.")
		return
	}
	if metadata == nil {
		handlers.RenderError(w, http.StatusNotFound, "no android credentials configured for this identifier")
		return
	}
	marshaledResponse, _ := json.Marshal(metadata)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

func (h *CredentialsHandler) PutAndroidCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	appId, identifierId := credentialsVars(w, r)
	if identifierId == "" {
		return
	}
	var requestBody struct {
		KeyAlias                string `json:"keyAlias"`
		Keystore                string `json:"keystore"`
		KeystorePassword        string `json:"keystorePassword"`
		KeyPassword             string `json:"keyPassword"`
		GoogleServiceAccountKey string `json:"googleServiceAccountKey"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	err := h.credentialsService.SaveAndroidCredentials(r.Context(), appId, identifierId, services.AndroidCredentialsInput{
		KeyAlias:                    requestBody.KeyAlias,
		KeystoreBase64:              requestBody.Keystore,
		KeystorePassword:            requestBody.KeystorePassword,
		KeyPassword:                 requestBody.KeyPassword,
		GoogleServiceAccountKeyJSON: requestBody.GoogleServiceAccountKey,
	})
	if err != nil {
		renderCredentialsError(w, err, "An internal error occurred while saving android credentials.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CredentialsHandler) DeleteAndroidCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	appId, identifierId := credentialsVars(w, r)
	if identifierId == "" {
		return
	}
	err := h.credentialsService.DeleteAndroidCredentials(r.Context(), appId, identifierId)
	if err != nil {
		renderCredentialsError(w, err, "An internal error occurred while deleting android credentials.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
