package handlers

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"xprem/internal/handlers"
	"xprem/internal/repository"
	"xprem/internal/services"
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

// renderServiceError maps the service-layer error vocabulary shared by the
// vault handlers (validation, not-found, stateless) to HTTP statuses.
func renderServiceError(w http.ResponseWriter, err error, fallback string) {
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
	handlers.RenderError(w, http.StatusInternalServerError, fallback)
}

func (h *CredentialsHandler) GetAndroidCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	appId, identifierId := credentialsVars(w, r)
	if identifierId == "" {
		return
	}
	metadata, err := h.credentialsService.GetAndroidCredentialsMetadata(r.Context(), appId, identifierId)
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while fetching android credentials.")
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
		KeyAlias         string `json:"keyAlias"`
		Keystore         string `json:"keystore"`
		KeystorePassword string `json:"keystorePassword"`
		KeyPassword      string `json:"keyPassword"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	err := h.credentialsService.SaveAndroidCredentials(r.Context(), appId, identifierId, services.AndroidCredentialsInput{
		KeyAlias:         requestBody.KeyAlias,
		KeystoreBase64:   requestBody.Keystore,
		KeystorePassword: requestBody.KeystorePassword,
		KeyPassword:      requestBody.KeyPassword,
	})
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while saving android credentials.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CredentialsHandler) PutGooglePlayServiceAccountHandler(w http.ResponseWriter, r *http.Request) {
	appId, identifierId := credentialsVars(w, r)
	if identifierId == "" {
		return
	}
	var requestBody struct {
		ServiceAccountKey string `json:"serviceAccountKey"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.credentialsService.SaveGooglePlayServiceAccountKey(r.Context(), appId, identifierId, requestBody.ServiceAccountKey); err != nil {
		renderServiceError(w, err, "An internal error occurred while saving the google play service account key.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CredentialsHandler) DeleteGooglePlayServiceAccountHandler(w http.ResponseWriter, r *http.Request) {
	appId, identifierId := credentialsVars(w, r)
	if identifierId == "" {
		return
	}
	if err := h.credentialsService.DeleteGooglePlayServiceAccountKey(r.Context(), appId, identifierId); err != nil {
		renderServiceError(w, err, "An internal error occurred while deleting the google play service account key.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CredentialsHandler) GenerateAndroidCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	appId, identifierId := credentialsVars(w, r)
	if identifierId == "" {
		return
	}
	if err := h.credentialsService.GenerateAndroidCredentials(r.Context(), appId, identifierId); err != nil {
		renderServiceError(w, err, "An internal error occurred while generating android credentials.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CredentialsHandler) DownloadAndroidKeystoreHandler(w http.ResponseWriter, r *http.Request) {
	appId, identifierId := credentialsVars(w, r)
	if identifierId == "" {
		return
	}
	exported, err := h.credentialsService.ExportAndroidKeystore(r.Context(), appId, identifierId)
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while exporting the android keystore.")
		return
	}

	credentialsJSON, err := json.MarshalIndent(struct {
		KeyAlias         string `json:"keyAlias"`
		KeystorePassword string `json:"keystorePassword"`
		KeyPassword      string `json:"keyPassword"`
	}{
		KeyAlias:         exported.KeyAlias,
		KeystorePassword: exported.KeystorePassword,
		KeyPassword:      exported.KeyPassword,
	}, "", "  ")
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while exporting the android keystore.")
		return
	}

	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	keystoreFile, err := zipWriter.Create("keystore.jks")
	if err == nil {
		_, err = keystoreFile.Write(exported.Keystore)
	}
	if err == nil {
		certificateFile, createErr := zipWriter.Create("upload-certificate.pem")
		err = createErr
		if err == nil {
			_, err = certificateFile.Write(exported.CertificatePEM)
		}
	}
	if err == nil {
		credentialsFile, createErr := zipWriter.Create("credentials.json")
		err = createErr
		if err == nil {
			_, err = credentialsFile.Write(credentialsJSON)
		}
	}
	if closeErr := zipWriter.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while exporting the android keystore.")
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="android-upload-keystore.zip"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", archive.Len()))
	w.Header().Set("Cache-Control", "private, no-cache, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(archive.Bytes())
}

func (h *CredentialsHandler) DeleteAndroidCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	appId, identifierId := credentialsVars(w, r)
	if identifierId == "" {
		return
	}
	err := h.credentialsService.DeleteAndroidCredentials(r.Context(), appId, identifierId)
	if err != nil {
		renderServiceError(w, err, "An internal error occurred while deleting android credentials.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
