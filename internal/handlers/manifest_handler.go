package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"
	"xprem/config"
	"xprem/internal/services"
	"xprem/internal/types"

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
	// Rejected marks a poll the server answered with an error. Such a poll is
	// no evidence that a device exists and must leave nothing durable behind,
	// so the recorder keeps only the crash detail, in memory, to re-attach to
	// the next poll that does resolve. Only set on a poll carrying one: every
	// other signal a check-in holds survives on its own (the failed-id header
	// is sticky, the device re-registers on its next poll), while the crash
	// detail is sent once and is gone if this request drops it.
	Rejected bool
	// Hardware and OS of the device, spelled as expo-device spells them, plus
	// the store version of the binary. Telemetry-only: the manifest headers
	// carry nothing of the sort, so these are empty on every poll and empty
	// always means "not reported", never "changed to empty".
	DeviceModel string
	OSName      string
	OSVersion   string
	AppVersion  string
	// ObservedAt is when the device was running CurrentUpdateID. Zero means
	// "as this arrives", which is what a manifest poll means: it answers the
	// device live. Telemetry sets it to the newest record of the batch, which
	// may be much older, and that is the difference the registry needs to stop
	// a late backlog from overwriting fresher news.
	ObservedAt time.Time
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

// reportRejectedCrash hands the one-shot crash detail of a refused poll to the
// registry seam, and only that: no remote address, so nothing resolves a place
// for a device we are not registering. A poll carrying no crash detail is not
// reported at all, which keeps a refused request exactly as free of side
// effects as it was. The caller decides WHEN to reach here, and only does so
// for a transient failure: there is nothing to hold a detail for when the
// answer is that the app does not exist.
func (h *ExpoProtocolHandler) reportRejectedCrash(r *http.Request, appId string, params services.ManifestRequestParams) {
	if h.onDeviceCheckIn == nil || params.ClientID == "" || params.ExpoFatalError == "" {
		return
	}
	h.onDeviceCheckIn(r.Context(), DeviceCheckIn{
		AppID:       appId,
		EASClientID: params.ClientID,
		FatalError:  params.ExpoFatalError,
		Rejected:    true,
	})
}

// manifestParams reads a manifest poll into its parameters. Kept apart from the
// handler so the header-to-field mapping is reachable without an HTTP round trip:
// every field below is a wire contract, and a dropped one fails silently.
// maxRequestedBranchLen bounds the branch a device may ask for. A name longer
// than one that could ever exist is not a branch, and this header is the only
// client-supplied value on the manifest path that reaches the query layer and
// the logs; its two siblings are both bounded before use.
const maxRequestedBranchLen = 128

// requestedBranch reads the surf header, dropping a value too long to name a
// branch. Dropped rather than rejected: an over-long header makes the request a
// plain poll, where refusing it would take an update away from a device whose
// only fault is a header nobody asked it to send.
func requestedBranch(r *http.Request) string {
	branchName := r.Header.Get("xprem-branch")
	if len(branchName) > maxRequestedBranchLen {
		return ""
	}
	return branchName
}

func manifestParams(r *http.Request, requestID string) (services.ManifestRequestParams, error) {
	appId := resolveAppID(r)
	if appId == "" {
		return services.ManifestRequestParams{}, errors.New("No app id provided")
	}

	channelName := r.Header.Get("expo-channel-name")
	if channelName == "" {
		return services.ManifestRequestParams{}, errors.New("No channel name provided")
	}

	protocolVersion, err := strconv.ParseInt(r.Header.Get("expo-protocol-version"), 10, 64)
	if err != nil {
		return services.ManifestRequestParams{}, errors.New("Invalid protocol version")
	}

	platform := r.Header.Get("expo-platform")
	if platform == "" {
		platform = r.URL.Query().Get("platform")
	}
	if platform != "ios" && platform != "android" {
		return services.ManifestRequestParams{}, errors.New("Invalid platform")
	}

	runtimeVersion := r.Header.Get("expo-runtime-version")
	if runtimeVersion == "" {
		runtimeVersion = r.URL.Query().Get("runtimeVersion")
	}
	if runtimeVersion == "" {
		return services.ManifestRequestParams{}, errors.New("No runtime version provided")
	}

	return services.ManifestRequestParams{
		RequestID:             requestID,
		AppID:                 appId,
		ChannelName:           channelName,
		Platform:              platform,
		RuntimeVersion:        runtimeVersion,
		ProtocolVersion:       protocolVersion,
		XpremBranch:           requestedBranch(r),
		SurfBlockTokens:       r.Header.Get(services.SurfBlockedHeader),
		ClientID:              r.Header.Get("EAS-Client-ID"),
		CurrentUpdateID:       r.Header.Get("expo-current-update-id"),
		ExpoFatalError:        r.Header.Get("expo-fatal-error"),
		RecentFailedUpdateIDs: r.Header.Get("Expo-Recent-Failed-Update-Ids"),
	}, nil
}

func serverDefinedHeaders(params services.ManifestRequestParams, result services.ManifestResult) *services.HeaderDictionary {
	dictionary := services.NewHeaderDictionary()
	if result.BlockedSurf != nil {
		services.SetSurfBlocked(dictionary, params.SurfBlockTokens, result.BlockedSurf.Token)
	}
	return dictionary
}

func writeServerDefinedHeaders(w http.ResponseWriter, dictionary *services.HeaderDictionary) {
	if encoded := dictionary.Encode(); encoded != "" {
		w.Header().Set(services.ServerDefinedHeadersHeader, encoded)
	}
}

func (h *ExpoProtocolHandler) HandleManifest(w http.ResponseWriter, r *http.Request) {
	requestID := uuid.New().String()

	params, err := manifestParams(r, requestID)
	if err != nil {
		log.Printf("[RequestID: %s] %v", requestID, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	appId := params.AppID
	platform := params.Platform
	runtimeVersion := params.RuntimeVersion
	protocolVersion := params.ProtocolVersion

	result, err := h.protocolService.ResolveManifestBundle(r.Context(), params)
	if err != nil {
		var svcErr *services.ExpoProtocolError
		status := http.StatusInternalServerError
		message := "Internal operational error"
		if errors.As(err, &svcErr) {
			status, message = svcErr.StatusCode, svcErr.Message
		}
		// The crash detail is the one thing this poll carries that no later
		// poll can, so a resolution that failed TRANSIENTLY still hands it
		// over: the recorder holds it in memory and re-attaches it to the next
		// poll that resolves, writing nothing durable now.
		//
		// Only transiently. A 4xx says the app or the channel does not exist,
		// and that answer will not change on its own, so there is no later poll
		// to re-attach anything to. Stashing there would let an unknown app id
		// put an entry in the cache for an hour, once per request, which is a
		// growth path opened by the very mechanism meant to avoid writing.
		if status >= http.StatusInternalServerError {
			h.reportRejectedCrash(r, appId, params)
		}
		http.Error(w, message, status)
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
		h.onDeviceCheckIn(r.Context(), DeviceCheckIn{
			AppID:              appId,
			EASClientID:        params.ClientID,
			CurrentUpdateID:    params.CurrentUpdateID,
			FailedUpdateIDsRaw: params.RecentFailedUpdateIDs,
			FatalError:         params.ExpoFatalError,
		})
	}

	// Set before any response is written so it also rides a noUpdateAvailable,
	// which is what a device already holding the mapped branch gets.
	writeServerDefinedHeaders(w, serverDefinedHeaders(params, result))
	refusedBranch := ""
	if result.BlockedSurf != nil {
		refusedBranch = result.BlockedSurf.BranchName
	}

	if result.Update == nil {
		log.Printf("[RequestID: %s] No update found for runtimeVersion: %s in branch: %s", requestID, runtimeVersion, result.BranchName)
		h.protocolService.PutNoUpdateAvailableInResponse(w, r, appId, runtimeVersion, protocolVersion, requestID)
		return
	}

	updateType := result.UpdateType
	if updateType == types.NormalUpdate {
		h.protocolService.PutUpdateInResponse(w, r, appId, *result.Update, platform, protocolVersion, requestID, refusedBranch)
	} else {
		h.protocolService.PutRollbackInResponse(w, r, appId, *result.Update, platform, protocolVersion, requestID)
	}
}
