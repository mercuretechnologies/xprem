package handlers

import (
	"errors"
	"log"
	"net/http"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/validation"

	"github.com/google/uuid"
)

// BranchListHandler serves the branches a device may ask to be served instead
// of the one its channel maps to. Device-facing and unauthenticated like the
// rest of the client protocol: the app id, the channel and the runtime version
// are the only claims a device makes, and the channel's branch-surfing setting
// decides whether it is answered at all.
type BranchListHandler struct {
	channelService *services.ChannelService
}

func NewBranchListHandler(channelService *services.ChannelService) *BranchListHandler {
	return &BranchListHandler{channelService: channelService}
}

func (h *BranchListHandler) HandleBranchList(w http.ResponseWriter, r *http.Request) {
	requestID := uuid.New().String()

	appId := resolveAppID(r)
	if appId == "" {
		log.Printf("[RequestID: %s] No app id provided", requestID)
		http.Error(w, "No app id provided", http.StatusBadRequest)
		return
	}

	channelName := r.Header.Get("expo-channel-name")
	if channelName == "" {
		log.Printf("[RequestID: %s] No channel name provided", requestID)
		http.Error(w, "No channel name provided", http.StatusBadRequest)
		return
	}

	runtimeVersion := r.Header.Get("expo-runtime-version")
	if runtimeVersion == "" {
		runtimeVersion = r.URL.Query().Get("runtimeVersion")
	}
	if runtimeVersion == "" {
		log.Printf("[RequestID: %s] No runtime version provided", requestID)
		http.Error(w, "No runtime version provided", http.StatusBadRequest)
		return
	}

	branches, err := h.channelService.ListSurfableBranches(r.Context(), appId, channelName, runtimeVersion)
	if err != nil {
		status, message := branchListErrorResponse(err)
		log.Printf("[RequestID: %s] Branch list refused for channel %s: %v", requestID, channelName, err)
		http.Error(w, message, status)
		return
	}

	// Same as the manifest response: the answer depends on headers a shared
	// cache does not key on, so it must never be stored.
	w.Header().Set("cache-control", "private, max-age=0")
	RenderJSON(w, http.StatusOK, branches)
}

func branchListErrorResponse(err error) (int, string) {
	var svcErr *services.ExpoProtocolError
	if errors.As(err, &svcErr) {
		return svcErr.StatusCode, svcErr.Message
	}
	if validation.IsValidationError(err) {
		return http.StatusBadRequest, err.Error()
	}
	if errors.Is(err, store.ErrNotSupportedInStatelessMode) {
		return http.StatusNotFound, "Branch surfing is not enabled for this channel"
	}
	return http.StatusInternalServerError, "Internal operational error"
}
