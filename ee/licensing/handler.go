// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"xprem/internal/handlers"
)

type LicenseHandler struct {
	service *LicenseService
}

func NewLicenseHandler(service *LicenseService) *LicenseHandler {
	return &LicenseHandler{service: service}
}

// LicenseResponse is the public shape of the deployment's license status.
// Valid is the single source of truth for "enterprise features are on":
// HasKey can be true with Valid false when the stored key is expired or
// malformed, in which case Error says why. ExpiresAt is empty for a
// perpetual license.
type LicenseResponse struct {
	HasKey      bool   `json:"hasKey"`
	Valid       bool   `json:"valid"`
	Error       string `json:"error,omitempty"`
	LicenseId   string `json:"licenseId,omitempty"`
	IssuedAt    string `json:"issuedAt,omitempty"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
	ActivatedAt string `json:"activatedAt,omitempty"`
}

func licenseResponseFrom(status LicenseStatus) LicenseResponse {
	response := LicenseResponse{
		HasKey: status.HasKey,
		Valid:  status.Valid(),
	}
	if status.Err != nil {
		response.Error = status.Err.Error()
	}
	if !status.ActivatedAt.IsZero() {
		response.ActivatedAt = status.ActivatedAt.UTC().Format(time.RFC3339)
	}
	if status.License != nil {
		response.LicenseId = status.License.LicenseID
		if !status.License.Created.IsZero() {
			response.IssuedAt = status.License.Created.UTC().Format(time.RFC3339)
		}
		if status.License.Expiry != nil {
			response.ExpiresAt = status.License.Expiry.UTC().Format(time.RFC3339)
		}
	}
	return response
}

// renderLicenseServiceError maps key-validation failures onto 400s with the
// actionable reason; anything unrecognized stays an opaque 500.
func renderLicenseServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrLicenseRequiresControlPlane),
		errors.Is(err, ErrMalformedKey),
		errors.Is(err, ErrInvalidSignature),
		errors.Is(err, ErrExpired),
		errors.Is(err, ErrNoVerifyKey):
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
	default:
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
	}
}

func (h *LicenseHandler) GetLicenseHandler(w http.ResponseWriter, r *http.Request) {
	status, err := h.service.Status(r.Context())
	if err != nil {
		renderLicenseServiceError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, licenseResponseFrom(status))
}

func (h *LicenseHandler) ActivateLicenseHandler(w http.ResponseWriter, r *http.Request) {
	var requestBody struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if requestBody.Key == "" {
		handlers.RenderError(w, http.StatusBadRequest, "key is required")
		return
	}
	status, err := h.service.Activate(r.Context(), requestBody.Key)
	if err != nil {
		renderLicenseServiceError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, licenseResponseFrom(status))
}

func (h *LicenseHandler) RemoveLicenseHandler(w http.ResponseWriter, r *http.Request) {
	if err := h.service.Remove(r.Context()); err != nil {
		renderLicenseServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
