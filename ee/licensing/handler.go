// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
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
// Valid stays true through the grace window and flips once it is exhausted.
type LicenseResponse struct {
	HasKey                bool   `json:"hasKey"`
	Valid                 bool   `json:"valid"`
	Suspended             bool   `json:"suspended,omitempty"`
	OrgName               string `json:"orgName,omitempty"`
	PlanCode              string `json:"planCode,omitempty"`
	SubscriptionStartAt   string `json:"subscriptionStartAt,omitempty"`
	SubscriptionEndAt     string `json:"subscriptionEndAt,omitempty"`
	SubscriptionRenewalAt string `json:"subscriptionRenewalAt,omitempty"`
	ActivatedAt           string `json:"activatedAt,omitempty"`
	LastValidatedAt       string `json:"lastValidatedAt,omitempty"`
	ValidationFailedAt    string `json:"validationFailedAt,omitempty"`
	ValidationErrorCode   string `json:"validationErrorCode,omitempty"`
	GraceEndsAt           string `json:"graceEndsAt,omitempty"`
}

// CheckLicenseResponse mirrors the license server's check outcome.
type CheckLicenseResponse struct {
	Valid                 bool   `json:"valid"`
	ErrorCode             string `json:"errorCode,omitempty"`
	OrgName               string `json:"orgName,omitempty"`
	PlanCode              string `json:"planCode,omitempty"`
	SubscriptionStartAt   string `json:"subscriptionStartAt,omitempty"`
	SubscriptionEndAt     string `json:"subscriptionEndAt,omitempty"`
	SubscriptionRenewalAt string `json:"subscriptionRenewalAt,omitempty"`
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatTime(*t)
}

func licenseResponseFrom(status LicenseStatus) LicenseResponse {
	response := LicenseResponse{
		HasKey:              status.HasKey,
		Valid:               status.Valid(),
		Suspended:           status.Suspended(),
		ActivatedAt:         formatTime(status.ActivatedAt),
		LastValidatedAt:     formatTimePtr(status.LastValidatedAt),
		ValidationFailedAt:  formatTimePtr(status.ValidationFailedAt),
		ValidationErrorCode: status.ValidationErrorCode,
		GraceEndsAt:         formatTimePtr(status.GraceDeadline()),
	}
	if status.License != nil {
		response.OrgName = status.License.OrgName
		response.PlanCode = status.License.PlanCode
		response.SubscriptionStartAt = formatTime(status.License.SubscriptionStartAt)
		response.SubscriptionEndAt = formatTimePtr(status.License.SubscriptionEndAt)
		response.SubscriptionRenewalAt = formatTimePtr(status.License.SubscriptionRenewalAt)
	}
	return response
}

func checkResponseFrom(result CheckResult) CheckLicenseResponse {
	response := CheckLicenseResponse{Valid: result.Valid, ErrorCode: result.ErrorCode}
	if result.License != nil {
		response.OrgName = result.License.OrgName
		response.PlanCode = result.License.PlanCode
		response.SubscriptionStartAt = formatTime(result.License.SubscriptionStartAt)
		response.SubscriptionEndAt = formatTimePtr(result.License.SubscriptionEndAt)
		response.SubscriptionRenewalAt = formatTimePtr(result.License.SubscriptionRenewalAt)
	}
	return response
}

func decisionMessage(code string) string {
	switch code {
	case CodeLicenseKeyNotFound:
		return "This license key does not exist."
	case CodeLicenseKeyAlreadyUsed:
		return "This license key has already been used. Contact support@xprem.dev to release it."
	case CodeLicenseKeyExpired:
		return "This license key expired before it was activated."
	case CodeInvalidInstanceURL:
		return "This server's URL (BASE_URL) is not allowed for this license."
	case CodeSubscriptionInactive:
		return "The subscription behind this license is inactive."
	case CodeLicenseExpired:
		return "The subscription behind this license has ended."
	case CodeInvalidActivation:
		return "The license server no longer recognizes this activation."
	case CodePlanNotSupported:
		return "This license is not on the Enterprise plan. Only Enterprise licenses are supported for now."
	default:
		return "The license server refused the request (" + code + ")."
	}
}

func renderLicenseServiceError(w http.ResponseWriter, err error) {
	var refusal *DecisionError
	switch {
	case errors.As(err, &refusal):
		handlers.RenderError(w, http.StatusBadRequest, decisionMessage(refusal.Code))
	case errors.Is(err, ErrServerRejected):
		handlers.RenderError(w, http.StatusBadGateway, "The license server rejected the request. Check the key and this server's BASE_URL, and contact support@xprem.dev if this persists.")
	case errors.Is(err, ErrServerUnreachable):
		handlers.RenderError(w, http.StatusBadGateway, "The license server could not be reached. Try again in a few minutes.")
	case errors.Is(err, ErrLicenseRequiresControlPlane),
		errors.Is(err, ErrInstanceIdUnavailable):
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("🚨 [LICENSE] Unexpected license operation error: %v", err)
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
	}
}

func decodeKeyBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var requestBody struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return "", false
	}
	key := strings.TrimSpace(requestBody.Key)
	if key == "" {
		handlers.RenderError(w, http.StatusBadRequest, "key is required")
		return "", false
	}
	return key, true
}

func (h *LicenseHandler) GetLicenseHandler(w http.ResponseWriter, r *http.Request) {
	status, err := h.service.Status(r.Context())
	if err != nil {
		renderLicenseServiceError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, licenseResponseFrom(status))
}

// CheckLicenseHandler answers 200 with valid false for an unusable key.
func (h *LicenseHandler) CheckLicenseHandler(w http.ResponseWriter, r *http.Request) {
	key, ok := decodeKeyBody(w, r)
	if !ok {
		return
	}
	result, err := h.service.Check(r.Context(), key)
	if err != nil {
		renderLicenseServiceError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, checkResponseFrom(result))
}

func (h *LicenseHandler) ActivateLicenseHandler(w http.ResponseWriter, r *http.Request) {
	key, ok := decodeKeyBody(w, r)
	if !ok {
		return
	}
	status, err := h.service.Attach(r.Context(), key)
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
