package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	cache2 "xprem/internal/cache"
	"xprem/internal/dashboard"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/types"
	update2 "xprem/internal/update"
	"xprem/internal/validation"

	"github.com/gorilla/mux"
)

type RolloutHandler struct {
	rolloutService *services.RolloutService
	updateService  *services.UpdateService
}

func NewRolloutHandler(rolloutService *services.RolloutService, updateService *services.UpdateService) *RolloutHandler {
	return &RolloutHandler{
		rolloutService: rolloutService,
		updateService:  updateService,
	}
}

// renderRolloutError maps the rollout service errors onto the RFC 7807 responses the
// dashboard expects; anything unrecognized falls back to a 500 with fallbackDetail.
func renderRolloutError(w http.ResponseWriter, err error, fallbackDetail string) {
	if errors.Is(err, services.ErrRolloutsRequireControlPlane) {
		handlers.RenderError(w, http.StatusBadRequest, "Progressive rollouts require the database control plane.")
		return
	}
	var valErr *validation.Error
	if errors.As(err, &valErr) {
		handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
		return
	}
	var reqErr *services.RolloutRequestError
	if errors.As(err, &reqErr) {
		handlers.RenderError(w, reqErr.Status, reqErr.Message)
		return
	}
	if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
		handlers.RenderError(w, http.StatusNotFound, notFoundErr.Error())
		return
	}
	if alreadyExistsErr := (*store.ErrResourceAlreadyExists)(nil); errors.As(err, &alreadyExistsErr) {
		handlers.RenderError(w, http.StatusConflict, "A rollout is already active on this channel. Promote or revert it before starting a new one.")
		return
	}
	handlers.RenderError(w, http.StatusInternalServerError, fallbackDetail)
}

// invalidateChannelRolloutCaches drops the dashboard listings that embed
// channel rollout state. The delivery path's own mapping cache is not touched:
// rollout changes reach devices within its TTL.
func invalidateChannelRolloutCaches(appId string) {
	cache := cache2.GetCache()
	cache.Delete(dashboard.ComputeGetChannelsCacheKey(appId))
	cache.Delete(dashboard.ComputeGetBranchesCacheKey(appId))
}

// invalidateUpdateRolloutCaches drops everything a per-update rollout mutation can
// have staled: the lastUpdate envelopes (which embed the rollout percentage and
// control), dashboard summaries, and the details of the affected rows. It then
// pre-warms both platform manifests so the next client hits warm caches.
func (h *RolloutHandler) invalidateUpdateRolloutCaches(appId string, branchName string, runtimeVersion string, affectedRollouts []types.RolloutUpdate) {
	cache := cache2.GetCache()
	cacheKeys := []string{
		update2.ComputeLastUpdateCacheKey(appId, branchName, runtimeVersion, "ios"),
		update2.ComputeLastUpdateCacheKey(appId, branchName, runtimeVersion, "android"),
		dashboard.ComputeGetRuntimeVersionsCacheKey(appId, branchName),
		dashboard.ComputeGetBranchesCacheKey(appId),
		dashboard.ComputeGetChannelsCacheKey(appId),
	}
	for _, affectedRollout := range affectedRollouts {
		cacheKeys = append(cacheKeys, dashboard.ComputeGetUpdateDetailsCacheKey(appId, branchName, runtimeVersion, affectedRollout.UpdateId))
		if affectedRollout.ControlUpdateId != nil {
			cacheKeys = append(cacheKeys, dashboard.ComputeGetUpdateDetailsCacheKey(appId, branchName, runtimeVersion, *affectedRollout.ControlUpdateId))
		}
	}
	for _, cacheKey := range cacheKeys {
		cache.Delete(cacheKey)
	}
	go services.PreWarmManifestCache(h.updateService, appId, branchName, runtimeVersion, "ios")
	go services.PreWarmManifestCache(h.updateService, appId, branchName, runtimeVersion, "android")
}

func (h *RolloutHandler) StartChannelRolloutHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	channelName := vars["CHANNEL"]
	var requestBody struct {
		BranchName string `json:"branchName"`
		Percentage int    `json:"percentage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if requestBody.BranchName == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Branch name is empty")
		return
	}
	channelRollout, err := h.rolloutService.StartChannelRollout(r.Context(), appId, channelName, requestBody.BranchName, requestBody.Percentage)
	if err != nil {
		renderRolloutError(w, err, "An internal error occurred while starting the rollout.")
		return
	}
	marshaledResponse, _ := json.Marshal(channelRollout)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(marshaledResponse)

	invalidateChannelRolloutCaches(appId)
}

func (h *RolloutHandler) UpdateChannelRolloutHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	channelName := vars["CHANNEL"]
	var requestBody struct {
		Percentage int `json:"percentage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	channelRollout, err := h.rolloutService.UpdateChannelRolloutPercentage(r.Context(), appId, channelName, requestBody.Percentage)
	if err != nil {
		renderRolloutError(w, err, "An internal error occurred while updating the rollout.")
		return
	}
	marshaledResponse, _ := json.Marshal(channelRollout)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)

	invalidateChannelRolloutCaches(appId)
}

func (h *RolloutHandler) EndChannelRolloutHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	channelName := vars["CHANNEL"]
	var requestBody struct {
		Outcome string `json:"outcome"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.rolloutService.EndChannelRollout(r.Context(), appId, channelName, requestBody.Outcome); err != nil {
		renderRolloutError(w, err, "An internal error occurred while ending the rollout.")
		return
	}
	w.WriteHeader(http.StatusNoContent)

	invalidateChannelRolloutCaches(appId)
}

func (h *RolloutHandler) GetUpdateRolloutHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	runtimeVersion := vars["RUNTIME_VERSION"]
	activeRollouts, err := h.rolloutService.GetUpdateRollout(r.Context(), appId, branchName, runtimeVersion)
	if err != nil {
		renderRolloutError(w, err, "An internal error occurred while fetching the rollout.")
		return
	}
	if activeRollouts == nil {
		activeRollouts = []types.RolloutUpdate{}
	}
	response := map[string]interface{}{
		"active":  len(activeRollouts) > 0,
		"updates": activeRollouts,
	}
	marshaledResponse, _ := json.Marshal(response)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

func (h *RolloutHandler) SetUpdateRolloutPercentageHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	runtimeVersion := vars["RUNTIME_VERSION"]
	var requestBody struct {
		Percentage       int     `json:"percentage"`
		ExpectedUpdateId *string `json:"expectedUpdateId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	affectedRollouts, err := h.rolloutService.SetUpdateRolloutPercentage(r.Context(), appId, branchName, runtimeVersion, requestBody.Percentage, requestBody.ExpectedUpdateId)
	if err != nil {
		renderRolloutError(w, err, "An internal error occurred while updating the rollout.")
		return
	}
	w.WriteHeader(http.StatusNoContent)

	h.invalidateUpdateRolloutCaches(appId, branchName, runtimeVersion, affectedRollouts)
}

func (h *RolloutHandler) RevertUpdateRolloutHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	runtimeVersion := vars["RUNTIME_VERSION"]
	var requestBody struct {
		ExpectedUpdateId *string `json:"expectedUpdateId"`
	}
	// The body is entirely optional here (expectedUpdateId is a stale-tab guard), so
	// an empty body must not 400.
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil && !errors.Is(err, io.EOF) {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	affectedRollouts, err := h.rolloutService.RevertUpdateRollout(r.Context(), appId, branchName, runtimeVersion, requestBody.ExpectedUpdateId)
	if err != nil {
		renderRolloutError(w, err, "An internal error occurred while reverting the rollout.")
		return
	}
	w.WriteHeader(http.StatusNoContent)

	h.invalidateUpdateRolloutCaches(appId, branchName, runtimeVersion, affectedRollouts)
}
