// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"xprem/internal/cache"
	"xprem/internal/dashboard"
	"xprem/internal/handlers"
	"xprem/internal/version"

	"github.com/gorilla/mux"
)

// Same shape as the internal/dashboard cache keys (appId included so entries
// from one app are never served to another); it lives here because the
// feature is enterprise code.
func computeGetApiKeyRestrictionsCacheKey(appId string) string {
	return fmt.Sprintf("dashboard:%s:%s:request:getApiKeyRestrictions", version.Version, appId)
}

type ApiKeyRestrictionHandler struct {
	service *ApiKeyRestrictionService
}

func NewApiKeyRestrictionHandler(service *ApiKeyRestrictionService) *ApiKeyRestrictionHandler {
	return &ApiKeyRestrictionHandler{service: service}
}

// ApiKeyRestrictionsResponse mirrors the dashboard's id conventions: ids are
// strings, like ApiKeyMetadata.ID.
type ApiKeyRestrictionsResponse struct {
	ApiKeyID                   string   `json:"apiKeyId"`
	CanAccessProtectedBranches bool     `json:"canAccessProtectedBranches"`
	AllowedIps                 []string `json:"allowedIps"`
}

func renderApiKeyRestrictionServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrRequiresControlPlane), errors.Is(err, ErrInvalidCidr):
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrRequiresValidLicense):
		handlers.RenderError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrApiKeyNotFound), errors.Is(err, ErrBranchNotFound):
		handlers.RenderError(w, http.StatusNotFound, err.Error())
	default:
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
	}
}

func (h *ApiKeyRestrictionHandler) GetApiKeyRestrictionsHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	requestCache := cache.GetCache()
	cacheKey := computeGetApiKeyRestrictionsCacheKey(appId)
	if cachedValue := requestCache.Get(cacheKey); cachedValue != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(cachedValue))
		return
	}
	restrictions, err := h.service.GetRestrictionsByApp(r.Context(), appId)
	if err != nil {
		renderApiKeyRestrictionServiceError(w, err)
		return
	}
	response := make([]ApiKeyRestrictionsResponse, 0, len(restrictions))
	for _, restriction := range restrictions {
		entry := ApiKeyRestrictionsResponse{
			ApiKeyID:                   strconv.FormatInt(restriction.ApiKeyID, 10),
			CanAccessProtectedBranches: restriction.CanAccessProtectedBranches,
			AllowedIps:                 make([]string, 0, len(restriction.AllowedIps)),
		}
		for _, prefix := range restriction.AllowedIps {
			entry.AllowedIps = append(entry.AllowedIps, prefix.String())
		}
		response = append(response, entry)
	}
	marshaledResponse, _ := json.Marshal(response)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)

	ttl := 60
	requestCache.Set(cacheKey, string(marshaledResponse), &ttl)
}

func (h *ApiKeyRestrictionHandler) SetApiKeyRestrictionsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	apiKeyID, err := strconv.ParseInt(vars["API_KEY_ID"], 10, 64)
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid API key ID")
		return
	}
	var req struct {
		CanAccessProtectedBranches bool     `json:"canAccessProtectedBranches"`
		AllowedIps                 []string `json:"allowedIps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if err := h.service.SetRestrictions(r.Context(), appId, apiKeyID, req.CanAccessProtectedBranches, req.AllowedIps); err != nil {
		renderApiKeyRestrictionServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)

	cache.GetCache().Delete(computeGetApiKeyRestrictionsCacheKey(appId))
}

func (h *ApiKeyRestrictionHandler) SetBranchProtectionHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	if branchName == "" {
		handlers.RenderError(w, http.StatusBadRequest, "No branch provided")
		return
	}
	var req struct {
		Protected bool `json:"protected"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if err := h.service.SetBranchProtection(r.Context(), appId, branchName, req.Protected); err != nil {
		renderApiKeyRestrictionServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)

	// The dashboard's branch listing carries the protected flag, so its
	// cached copy is stale now.
	cache.GetCache().Delete(dashboard.ComputeGetBranchesCacheKey(appId))
}
