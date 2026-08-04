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

const maxRuntimeVersionLen = 128

// SurfingDisabledHeader marks a 404 that came from this endpoint deciding the
// channel may not surf, rather than from anything else on the way.
const SurfingDisabledHeader = "xprem-branch-surfing"

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
	// Bounded because the answer is cached under it, empty answers included: an
	// unauthenticated caller varying this header would otherwise mint one cache
	// entry per request, and the local cache has no size ceiling. A version
	// string longer than this cannot match anything that was ever published.
	if runtimeVersion == "" || len(runtimeVersion) > maxRuntimeVersionLen {
		log.Printf("[RequestID: %s] No runtime version provided", requestID)
		http.Error(w, "No runtime version provided", http.StatusBadRequest)
		return
	}

	// Same rule as the manifest route, which resolves per platform: a list built
	// without it would offer branches whose only update is for the other one, and
	// picking those would quietly land the device back on its channel's branch.
	platform := r.Header.Get("expo-platform")
	if platform == "" {
		platform = r.URL.Query().Get("platform")
	}
	if platform != "ios" && platform != "android" {
		log.Printf("[RequestID: %s] Invalid platform: %s", requestID, platform)
		http.Error(w, "Invalid platform. Expected \"ios\" or \"android\".", http.StatusBadRequest)
		return
	}

	// Devices fetch the first page at every launch; "see all" is a deliberate tap
	// by one tester, so the wider answer is never on the launch path.
	all := r.URL.Query().Get("all") == "1"

	branches, err := h.channelService.ListSurfableBranches(r.Context(), appId, channelName, runtimeVersion, platform, all)
	if err != nil {
		status, message := branchListErrorResponse(err)
		log.Printf("[RequestID: %s] Branch list refused for channel %s: %v", requestID, channelName, err)
		if status == http.StatusNotFound {
			w.Header().Set(SurfingDisabledHeader, "off")
		}
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
