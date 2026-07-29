// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"encoding/json"
	"errors"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/validation"
	"expo-open-ota/internal/version"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
)

// computeGetApiKeyAccessCacheKey builds the cache key for one app's access list.
func computeGetApiKeyAccessCacheKey(appId string) string {
	return fmt.Sprintf("dashboard:%s:%s:request:getApiKeyAccess", version.Version, appId)
}

// maxAccessBodyBytes bounds the access payload size.
const maxAccessBodyBytes = 64 << 10

type ApiKeyAccessHandler struct {
	service *ApiKeyAccessService
}

func NewApiKeyAccessHandler(service *ApiKeyAccessService) *ApiKeyAccessHandler {
	return &ApiKeyAccessHandler{service: service}
}

// branchRulePayload is one rule on the wire; actions are plain strings so an
// unknown one gets a named 400 instead of a decoding error.
type branchRulePayload struct {
	Pattern string   `json:"pattern"`
	Actions []string `json:"actions"`
}

// ApiKeyAccessResponse mirrors the dashboard's id conventions: ids are
// strings, like ApiKeyMetadata.ID. An empty branchRules means the key reaches
// every branch.
type ApiKeyAccessResponse struct {
	ApiKeyID    string              `json:"apiKeyId"`
	BranchRules []branchRulePayload `json:"branchRules"`
	AllowedIps  []string            `json:"allowedIps"`
}

func renderApiKeyAccessServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrRequiresControlPlane), errors.Is(err, ErrInvalidCidr):
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrRequiresValidLicense):
		handlers.RenderError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrApiKeyNotFound):
		handlers.RenderError(w, http.StatusNotFound, err.Error())
	// A malformed rule is caller input, like a malformed CIDR above.
	case validation.IsValidationError(err):
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
	default:
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
	}
}

func (h *ApiKeyAccessHandler) GetApiKeyAccessHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	requestCache := cache.GetCache()
	cacheKey := computeGetApiKeyAccessCacheKey(appId)
	if cachedValue := requestCache.Get(cacheKey); cachedValue != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(cachedValue))
		return
	}
	accesses, err := h.service.GetAccessByApp(r.Context(), appId)
	if err != nil {
		renderApiKeyAccessServiceError(w, err)
		return
	}
	response := make([]ApiKeyAccessResponse, 0, len(accesses))
	for _, access := range accesses {
		entry := ApiKeyAccessResponse{
			ApiKeyID:    strconv.FormatInt(access.ApiKeyID, 10),
			BranchRules: make([]branchRulePayload, 0, len(access.BranchRules)),
			AllowedIps:  make([]string, 0, len(access.AllowedIps)),
		}
		for _, rule := range access.BranchRules {
			entry.BranchRules = append(entry.BranchRules, branchRulePayload{
				Pattern: rule.Pattern,
				Actions: fromActions(rule.Actions),
			})
		}
		for _, prefix := range access.AllowedIps {
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

func (h *ApiKeyAccessHandler) SetApiKeyAccessHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	apiKeyID, err := strconv.ParseInt(vars["API_KEY_ID"], 10, 64)
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid API key ID")
		return
	}
	var req struct {
		BranchRules []branchRulePayload `json:"branchRules"`
		AllowedIps  []string            `json:"allowedIps"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAccessBodyBytes)).Decode(&req); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	rules := make([]BranchRule, 0, len(req.BranchRules))
	for _, payload := range req.BranchRules {
		actions := make([]Action, 0, len(payload.Actions))
		for _, action := range payload.Actions {
			actions = append(actions, Action(action))
		}
		rules = append(rules, BranchRule{Pattern: payload.Pattern, Actions: actions})
	}
	if err := h.service.SetAccess(r.Context(), appId, apiKeyID, rules, req.AllowedIps); err != nil {
		renderApiKeyAccessServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)

	cache.GetCache().Delete(computeGetApiKeyAccessCacheKey(appId))
}
