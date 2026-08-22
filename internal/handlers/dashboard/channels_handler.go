package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	cache2 "xprem/internal/cache"
	"xprem/internal/dashboard"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/gorilla/mux"
)

type ChannelHandler struct {
	channelService *services.ChannelService
}

func NewChannelHandler(channelService *services.ChannelService) *ChannelHandler {
	return &ChannelHandler{
		channelService: channelService,
	}
}

func (h *ChannelHandler) CreateChannelHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	var requestBody struct {
		BranchName  *string `json:"branchName"`
		ChannelName string  `json:"channelName"`
	}
	err := json.NewDecoder(r.Body).Decode(&requestBody)
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if requestBody.ChannelName == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Channel name is empty")
		return
	}
	if requestBody.BranchName != nil && *requestBody.BranchName == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Branch name is empty")
		return
	}
	channelId, err := h.channelService.CreateChannel(r.Context(), appId, requestBody.BranchName, requestBody.ChannelName)
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
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while creating the channel.")
		return
	}
	marshaledResponse, _ := json.Marshal(createChannelResponse{ChannelId: strconv.FormatInt(channelId, 10)})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)

}

func (h *ChannelHandler) DeleteChannelHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	channelName := vars["CHANNEL"]
	if channelName == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Channel name is empty")
		return
	}
	err := h.channelService.DeleteChannel(r.Context(), channelName, appId)
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
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while deleting the channel.")
		return
	}
	w.WriteHeader(http.StatusNoContent)

}

func (h *ChannelHandler) SetBranchSurfingHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	channelName := vars["CHANNEL"]
	if channelName == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Channel name is empty")
		return
	}
	var requestBody struct {
		Enabled bool   `json:"enabled"`
		Pattern string `json:"pattern"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Required, never defaulted: a PUT replaces the whole setting, so filling a
	// missing pattern in would silently widen a channel that named a narrower
	// one. Exposing every branch has to be spelled "*".
	if requestBody.Pattern == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Pattern is empty; use \"*\" to expose every branch")
		return
	}
	err := h.channelService.SetBranchSurfing(r.Context(), appId, channelName, types.BranchSurfing{
		Enabled: requestBody.Enabled,
		Pattern: requestBody.Pattern,
	})
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
		if errors.Is(err, store.ErrNotSupportedInStatelessMode) {
			handlers.RenderError(w, http.StatusBadRequest, err.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while updating branch surfing.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ChannelHandler) GetChannelsHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	cacheKey := dashboard.ComputeGetChannelsCacheKey(appId)
	cache := cache2.GetCache()
	if cacheValue := cache.Get(cacheKey); cacheValue != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(cacheValue))
		return
	}
	channels, err := h.channelService.GetChannels(r.Context(), appId)
	if err != nil {
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while fetching channels.")
		return
	}
	marshaledResponse, _ := json.Marshal(channels)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)

	ttl := 3600
	cache.Set(cacheKey, string(marshaledResponse), &ttl)
}
