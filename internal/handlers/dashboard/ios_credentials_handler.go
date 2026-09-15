package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"xprem/internal/appstoreconnect"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type IosCredentialsHandler struct {
	iosCredentialsService *services.IosCredentialsService
}

// NewIosCredentialsHandler connects dashboard and public device routes to the iOS service.
func NewIosCredentialsHandler(iosCredentialsService *services.IosCredentialsService) *IosCredentialsHandler {
	return &IosCredentialsHandler{
		iosCredentialsService: iosCredentialsService,
	}
}

// renderIosServiceError maps Apple outages to 502 and delegates other service errors.
func renderIosServiceError(w http.ResponseWriter, err error, fallback string) {
	if errors.Is(err, appstoreconnect.ErrUnavailable) {
		handlers.RenderError(w, http.StatusBadGateway, "App Store Connect could not be reached. Try again in a few minutes.")
		return
	}
	renderServiceError(w, err, fallback)
}

// uuidVar reads a uuid route var; "" means the response was already written.
func uuidVar(w http.ResponseWriter, r *http.Request, name string, label string) string {
	value := mux.Vars(r)[name]
	if _, err := uuid.Parse(value); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid "+label)
		return ""
	}
	return value
}

// decodeIosBody decodes at most 2 MiB of JSON, writing a 400 response on failure.
func decodeIosBody(w http.ResponseWriter, r *http.Request, body any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(body); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// renderJSON returns an HTTP 200 JSON response.
func renderJSON(w http.ResponseWriter, payload any) {
	marshaledResponse, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

// renderIosMetadata answers a successful identifier write with the identifier's signing state.
func (h *IosCredentialsHandler) renderIosMetadata(w http.ResponseWriter, r *http.Request, appId string, identifierId string) {
	metadata, err := h.iosCredentialsService.GetIosCredentialsMetadata(r.Context(), appId, identifierId)
	if err != nil {
		renderIosServiceError(w, err, "An internal error occurred while fetching ios credentials.")
		return
	}
	renderJSON(w, metadata)
}

// GetAppStoreConnectApiKeyHandler returns API key metadata without the private key.
func (h *IosCredentialsHandler) GetAppStoreConnectApiKeyHandler(w http.ResponseWriter, r *http.Request) {
	metadata, err := h.iosCredentialsService.GetAppStoreConnectApiKeyMetadata(r.Context(), mux.Vars(r)["APP_ID"])
	if err != nil {
		renderIosServiceError(w, err, "An internal error occurred while fetching the App Store Connect API key.")
		return
	}
	renderJSON(w, map[string]any{"apiKey": metadata})
}

// PutAppStoreConnectApiKeyHandler validates and saves the team API key, then returns its metadata.
func (h *IosCredentialsHandler) PutAppStoreConnectApiKeyHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	var requestBody struct {
		KeyID      string `json:"keyId"`
		IssuerID   string `json:"issuerId"`
		PrivateKey string `json:"privateKey"`
	}
	if !decodeIosBody(w, r, &requestBody) {
		return
	}
	err := h.iosCredentialsService.SaveAppStoreConnectApiKey(r.Context(), appId, services.AppStoreConnectApiKeyInput{
		KeyID:      requestBody.KeyID,
		IssuerID:   requestBody.IssuerID,
		PrivateKey: requestBody.PrivateKey,
	})
	if err != nil {
		renderIosServiceError(w, err, "An internal error occurred while saving the App Store Connect API key.")
		return
	}
	h.GetAppStoreConnectApiKeyHandler(w, r)
}

// DeleteAppStoreConnectApiKeyHandler removes the app API key and returns HTTP 204.
func (h *IosCredentialsHandler) DeleteAppStoreConnectApiKeyHandler(w http.ResponseWriter, r *http.Request) {
	if err := h.iosCredentialsService.DeleteAppStoreConnectApiKey(r.Context(), mux.Vars(r)["APP_ID"]); err != nil {
		renderIosServiceError(w, err, "An internal error occurred while deleting the App Store Connect API key.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetIosCredentialsHandler returns the identifier signing mode and selected certificate.
func (h *IosCredentialsHandler) GetIosCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	appId, identifierId := credentialsVars(w, r)
	if identifierId == "" {
		return
	}
	h.renderIosMetadata(w, r, appId, identifierId)
}

// PutIosSigningSettingHandler saves an identifier signing choice and returns the updated state.
func (h *IosCredentialsHandler) PutIosSigningSettingHandler(w http.ResponseWriter, r *http.Request) {
	appId, identifierId := credentialsVars(w, r)
	if identifierId == "" {
		return
	}
	var requestBody struct {
		Mode          types.IosSigningMode `json:"mode"`
		CertificateID string               `json:"certificateId"`
	}
	if !decodeIosBody(w, r, &requestBody) {
		return
	}
	err := h.iosCredentialsService.UpdateIosSigningSetting(r.Context(), appId, identifierId, services.IosSigningSettingInput{
		Mode:          requestBody.Mode,
		CertificateId: requestBody.CertificateID,
	})
	if err != nil {
		renderIosServiceError(w, err, "An internal error occurred while saving the ios signing setting.")
		return
	}
	h.renderIosMetadata(w, r, appId, identifierId)
}

// ListIosSigningCertificatesHandler lists Apple certificates with their local private-key availability.
func (h *IosCredentialsHandler) ListIosSigningCertificatesHandler(w http.ResponseWriter, r *http.Request) {
	appId, identifierId := credentialsVars(w, r)
	if identifierId == "" {
		return
	}
	certificates, err := h.iosCredentialsService.ListIosSigningCertificates(r.Context(), appId, identifierId)
	if err != nil {
		renderIosServiceError(w, err, "An internal error occurred while listing Apple certificates.")
		return
	}
	renderJSON(w, map[string]any{"certificates": certificates})
}

// ImportIosCertificateHandler imports a matching PKCS#12 identity and returns the certificate list.
func (h *IosCredentialsHandler) ImportIosCertificateHandler(w http.ResponseWriter, r *http.Request) {
	appId, identifierId := credentialsVars(w, r)
	if identifierId == "" {
		return
	}
	var requestBody struct {
		FingerprintSHA1     string `json:"fingerprintSha1"`
		CertificateP12      string `json:"certificateP12"`
		CertificatePassword string `json:"certificatePassword"`
	}
	if !decodeIosBody(w, r, &requestBody) {
		return
	}
	certificates, err := h.iosCredentialsService.ImportIosCertificate(r.Context(), appId, identifierId, services.IosCertificateImportInput{
		FingerprintSHA1:      requestBody.FingerprintSHA1,
		CertificateP12Base64: requestBody.CertificateP12,
		CertificatePassword:  requestBody.CertificatePassword,
	})
	if err != nil {
		renderIosServiceError(w, err, "An internal error occurred while importing the ios certificate.")
		return
	}
	renderJSON(w, map[string]any{"certificates": certificates})
}
