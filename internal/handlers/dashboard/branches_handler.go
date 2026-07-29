package handlers

import (
	"encoding/json"
	"errors"
	cache2 "expo-open-ota/internal/cache"
	"expo-open-ota/internal/dashboard"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/providers/expo"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"
	"expo-open-ota/internal/validation"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
)

type BranchHandler struct {
	branchService *services.BranchService
}

func NewBranchHandler(branchService *services.BranchService) *BranchHandler {
	return &BranchHandler{
		branchService: branchService,
	}
}

func (h *BranchHandler) CreateBranchHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	var requestBody struct {
		BranchName string `json:"branchName"`
	}
	err := json.NewDecoder(r.Body).Decode(&requestBody)
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if requestBody.BranchName == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Branch name is empty")
		return
	}
	branchId, err := h.branchService.CreateBranch(r.Context(), appId, requestBody.BranchName)
	if err != nil {
		var valErr *validation.Error
		if errors.As(err, &valErr) {
			handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
			return
		}
		if alreadyExistsErr := (*store.ErrResourceAlreadyExists)(nil); errors.As(err, &alreadyExistsErr) {
			handlers.RenderError(w, http.StatusConflict, alreadyExistsErr.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while creating the branch.")
		return
	}
	marshaledResponse, _ := json.Marshal(map[string]interface{}{
		"branchId": strconv.FormatInt(branchId, 10),
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)

	cache := cache2.GetCache()
	branchesCacheKey := dashboard.ComputeGetBranchesCacheKey(appId)
	cache.Delete(branchesCacheKey)
}

func (h *BranchHandler) DeleteBranchHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	if branchName == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Branch name is empty")
		return
	}
	err := h.branchService.DeleteBranch(r.Context(), branchName, appId)
	if err != nil {
		var valErr *validation.Error
		if errors.As(err, &valErr) {
			handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
			return
		}
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			handlers.RenderError(w, http.StatusNotFound, notFoundErr.Error())
			return
		}
		if branchErr := (*store.ErrBranchHasActiveChannels)(nil); errors.As(err, &branchErr) {
			handlers.RenderError(w, http.StatusConflict, branchErr.Error())
			return
		}
		if protectedErr := (*store.ErrBranchProtected)(nil); errors.As(err, &protectedErr) {
			handlers.RenderError(w, http.StatusConflict, protectedErr.Error())
			return
		}
		if rolloutErr := (*store.ErrBranchInActiveRollout)(nil); errors.As(err, &rolloutErr) {
			handlers.RenderError(w, http.StatusConflict, rolloutErr.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while deleting the branch.")
		return
	}
	w.WriteHeader(http.StatusNoContent)

	cache := cache2.GetCache()
	branchesCacheKey := dashboard.ComputeGetBranchesCacheKey(appId)
	runtimeCacheKey := dashboard.ComputeGetRuntimeVersionsCacheKey(appId, branchName)
	cache.Delete(branchesCacheKey)
	cache.Delete(runtimeCacheKey)
}

func (h *BranchHandler) GetBranchesHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	cache := cache2.GetCache()
	cacheKey := dashboard.ComputeGetBranchesCacheKey(appId)
	if cacheValue := cache.Get(cacheKey); cacheValue != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(cacheValue))
		return
	}
	branches, err := h.branchService.GetBranches(r.Context(), appId)
	if err != nil {
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while fetching branches.")
		return
	}

	marshaledResponse, _ := json.Marshal(branches)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)

	ttl := 3600
	cache.Set(cacheKey, string(marshaledResponse), &ttl)
}

func (h *BranchHandler) GetRuntimeVersionsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	if branchName == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Branch name is empty")
		return
	}
	cacheKey := dashboard.ComputeGetRuntimeVersionsCacheKey(appId, branchName)
	cache := cache2.GetCache()
	if cacheValue := cache.Get(cacheKey); cacheValue != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(cacheValue))
		return
	}
	runtimeVersions, err := h.branchService.GetRuntimeVersionsWithUpdateStats(r.Context(), appId, branchName)
	if err != nil {
		var valErr *validation.Error
		if errors.As(err, &valErr) {
			handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while fetching runtime versions.")
		return
	}
	marshaledResponse, _ := json.Marshal(runtimeVersions)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)

	ttl := 3600
	cache.Set(cacheKey, string(marshaledResponse), &ttl)
}

func (h *BranchHandler) UpdateChannelBranchMappingHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchId := vars["BRANCH_ID"]
	// Both identifiers are needed and they are not interchangeable: the remap
	// itself is keyed by the channel's *id* (numeric on the control plane, the
	// provider's channel id on the bucket backend), while the Expo channel
	// mapping cache is keyed by the channel's *name*.
	var requestBody struct {
		ReleaseChannelId   string `json:"releaseChannelId"`
		ReleaseChannelName string `json:"releaseChannelName"`
	}
	err := json.NewDecoder(r.Body).Decode(&requestBody)
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	releaseChannelId := requestBody.ReleaseChannelId
	if releaseChannelId == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Release channel id is empty")
		return
	}
	releaseChannelName := requestBody.ReleaseChannelName
	if releaseChannelName == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Release channel name is empty")
		return
	}
	if branchId == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Branch ID is empty")
		return
	}
	err = h.branchService.UpdateChannelBranchMapping(r.Context(), appId, releaseChannelId, releaseChannelName, branchId)
	if err != nil {
		var valErr *validation.Error
		if errors.As(err, &valErr) {
			handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
			return
		}
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			handlers.RenderError(w, http.StatusNotFound, notFoundErr.Error())
			return
		}
		if rolloutErr := (*store.ErrChannelHasActiveRollout)(nil); errors.As(err, &rolloutErr) {
			handlers.RenderError(w, http.StatusConflict, rolloutErr.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while updating the channel-branch mapping.")
		return
	}
	w.WriteHeader(http.StatusNoContent)

	channelsCacheKey := dashboard.ComputeGetChannelsCacheKey(appId)
	branchesCacheKey := dashboard.ComputeGetBranchesCacheKey(appId)
	// Keyed by name: this is the cache FetchExpoChannelMapping writes, and
	// remapping the channel is exactly what makes its entry stale.
	channelMappingCacheKey := expo.ComputeChannelMappingCacheKey(appId, releaseChannelName)
	cache := cache2.GetCache()
	cache.Delete(channelsCacheKey)
	cache.Delete(branchesCacheKey)
	cache.Delete(channelMappingCacheKey)
}
