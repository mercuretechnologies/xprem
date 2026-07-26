package handlers

import (
	"context"
	"errors"
	"expo-open-ota/config"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/types"
	"log"
	"net/http"
	"strconv"

	"github.com/google/uuid"
)

// DeviceCheckIn is everything one manifest poll tells the device registry:
// who polled, from where, what it currently runs, and which updates crashed
// on it. The seam's vocabulary type, MIT-side on purpose (the leaf-vocabulary
// pattern): the wired EE recorder consumes it without the handler importing
// any EE package.
type DeviceCheckIn struct {
	AppID       string
	EASClientID string
	RemoteIP    string
	// CurrentUpdateID is the update the device is RUNNING (the launched
	// update: one that crashed at launch never appears here). A device on the
	// embedded bundle reports the embedded update's OWN id, which no updates
	// table row matches; absent only on clients that sent no header.
	CurrentUpdateID string
	// FailedUpdateIDsRaw is the Expo-Recent-Failed-Update-IDs header verbatim
	// (a structured-field list of quoted UUIDs); parsing belongs to the
	// consumer.
	FailedUpdateIDsRaw string
	// FatalError is the Expo-Fatal-Error header: the crash detail, sent by
	// the client exactly once, on the first poll after the crash.
	FatalError string
	// Hardware and OS of the device, spelled as expo-device spells them.
	// Telemetry-only: the manifest headers carry nothing of the sort, so
	// these are empty on every poll and empty always means "not reported",
	// never "changed to empty".
	DeviceModel string
	OSName      string
	OSVersion   string
}

type ExpoProtocolHandler struct {
	protocolService *services.ExpoProtocolService
	// onDeviceCheckIn, when wired, registers the polling device in the universal
	// device registry (the Observe feature's). A method-value seam like the
	// audit recorder: the composition root wires it when Observe is enabled,
	// and it must never block or fail the manifest path (the wired side runs
	// its registry write in the background).
	onDeviceCheckIn func(ctx context.Context, checkIn DeviceCheckIn)
}

func NewExpoProtocolHandler(ps *services.ExpoProtocolService) *ExpoProtocolHandler {
	return &ExpoProtocolHandler{protocolService: ps}
}

// SetOnDeviceCheckIn wires the device check-in recorder; nil (never called) keeps
// manifest polls side-effect free.
func (h *ExpoProtocolHandler) SetOnDeviceCheckIn(fn func(ctx context.Context, checkIn DeviceCheckIn)) {
	h.onDeviceCheckIn = fn
}

// resolveAppID returns the app a manifest or asset request targets. The
// expo-app-id header wins when present. When it is absent the caller is a v1
// client that cannot send it, so we fall back to the deploy's legacy app:
// see config.LegacyFallbackAppId, which returns "" when there is none and
// leaves the request to be rejected.
func resolveAppID(r *http.Request) string {
	if appId := r.Header.Get("expo-app-id"); appId != "" {
		return appId
	}
	return config.LegacyFallbackAppId()
}

func (h *ExpoProtocolHandler) HandleManifest(w http.ResponseWriter, r *http.Request) {
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

	protocolVersion, err := strconv.ParseInt(r.Header.Get("expo-protocol-version"), 10, 64)
	if err != nil {
		log.Printf("[RequestID: %s] Invalid protocol version: %v", requestID, err)
		http.Error(w, "Invalid protocol version", http.StatusBadRequest)
		return
	}

	platform := r.Header.Get("expo-platform")
	if platform == "" {
		platform = r.URL.Query().Get("platform")
	}
	if platform != "ios" && platform != "android" {
		log.Printf("[RequestID: %s] Invalid platform: %s", requestID, platform)
		http.Error(w, "Invalid platform", http.StatusBadRequest)
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

	params := services.ManifestRequestParams{
		RequestID:             requestID,
		AppID:                 appId,
		ChannelName:           channelName,
		Platform:              platform,
		RuntimeVersion:        runtimeVersion,
		ProtocolVersion:       protocolVersion,
		ClientID:              r.Header.Get("EAS-Client-ID"),
		CurrentUpdateID:       r.Header.Get("expo-current-update-id"),
		ExpoFatalError:        r.Header.Get("expo-fatal-error"),
		RecentFailedUpdateIDs: r.Header.Get("Expo-Recent-Failed-Update-Ids"),
	}

	result, err := h.protocolService.ResolveManifestBundle(r.Context(), params)
	if err != nil {
		var svcErr *services.ExpoProtocolError
		if errors.As(err, &svcErr) {
			http.Error(w, svcErr.Message, svcErr.StatusCode)
			return
		}
		http.Error(w, "Internal operational error", http.StatusInternalServerError)
		return
	}

	// A poll becomes a device check-in only once it has resolved: a request we
	// answer with an error is not evidence that a device exists, and the
	// registry is a durable table reachable from an unauthenticated endpoint.
	// Resolving first means an unknown app id or a channel that maps to no
	// branch is rejected without leaving a row behind. A resolution that found
	// no update still checks in: the app, the channel and the branch were all
	// real, and a device out of a rollout bucket or ahead of every published
	// update is as alive as any other. The check-in carries the update-health
	// signals the same headers already delivered.
	if h.onDeviceCheckIn != nil && params.ClientID != "" {
		remoteIP := ""
		if clientIP := helpers.ClientIP(r); clientIP.IsValid() {
			remoteIP = clientIP.String()
		}
		h.onDeviceCheckIn(r.Context(), DeviceCheckIn{
			AppID:              appId,
			EASClientID:        params.ClientID,
			RemoteIP:           remoteIP,
			CurrentUpdateID:    params.CurrentUpdateID,
			FailedUpdateIDsRaw: params.RecentFailedUpdateIDs,
			FatalError:         params.ExpoFatalError,
		})
	}

	if result.Update == nil {
		log.Printf("[RequestID: %s] No update found for runtimeVersion: %s in branch: %s", requestID, runtimeVersion, result.BranchName)
		h.protocolService.PutNoUpdateAvailableInResponse(w, r, appId, runtimeVersion, protocolVersion, requestID)
		return
	}

	updateType := result.UpdateType
	if updateType == types.NormalUpdate {
		h.protocolService.PutUpdateInResponse(w, r, appId, *result.Update, platform, protocolVersion, requestID)
	} else {
		h.protocolService.PutRollbackInResponse(w, r, appId, *result.Update, platform, protocolVersion, requestID)
	}
}
