package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/validation"

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

func (h *CredentialsHandler) GetAndroidCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	metadata, err := h.credentialsService.GetAndroidCredentialsMetadata(r.Context(), appId)
	if err != nil {
		if errors.Is(err, store.ErrNotSupportedInStatelessMode) {
			handlers.RenderError(w, http.StatusBadRequest, err.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while fetching android credentials.")
		return
	}
	if metadata == nil {
		handlers.RenderError(w, http.StatusNotFound, "no android credentials configured for this app")
		return
	}
	marshaledResponse, _ := json.Marshal(metadata)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

func (h *CredentialsHandler) PutAndroidCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	var requestBody struct {
		AndroidPackage          string `json:"androidPackage"`
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
	err := h.credentialsService.SaveAndroidCredentials(r.Context(), appId, services.AndroidCredentialsInput{
		AndroidPackage:              requestBody.AndroidPackage,
		KeyAlias:                    requestBody.KeyAlias,
		KeystoreBase64:              requestBody.Keystore,
		KeystorePassword:            requestBody.KeystorePassword,
		KeyPassword:                 requestBody.KeyPassword,
		GoogleServiceAccountKeyJSON: requestBody.GoogleServiceAccountKey,
	})
	if err != nil {
		var valErr *validation.Error
		if errors.As(err, &valErr) {
			handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
			return
		}
		if errors.Is(err, store.ErrNotSupportedInStatelessMode) {
			handlers.RenderError(w, http.StatusBadRequest, err.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while saving android credentials.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CredentialsHandler) DeleteAndroidCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	err := h.credentialsService.DeleteAndroidCredentials(r.Context(), appId)
	if err != nil {
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			handlers.RenderError(w, http.StatusNotFound, notFoundErr.Error())
			return
		}
		if errors.Is(err, store.ErrNotSupportedInStatelessMode) {
			handlers.RenderError(w, http.StatusBadRequest, err.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while deleting android credentials.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
