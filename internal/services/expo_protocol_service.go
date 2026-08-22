package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"
	"xprem/config"
	"xprem/internal/assets"
	"xprem/internal/branch"
	cache2 "xprem/internal/cache"
	cdn2 "xprem/internal/cdn"
	"xprem/internal/crypto"
	"xprem/internal/keyStore"
	"xprem/internal/metrics"
	"xprem/internal/providers/expo"
	"xprem/internal/types"
	update2 "xprem/internal/update"
)

type ExpoProtocolService struct {
	appRepo       AppRepository
	channelRepo   ChannelRepository
	updateRepo    UpdateRepository
	updateService *UpdateService
	branchRules   []BranchRule
}

type ManifestRequestParams struct {
	RequestID             string
	AppID                 string
	ChannelName           string
	Platform              string
	RuntimeVersion        string
	ProtocolVersion       int64
	ClientID              string
	CurrentUpdateID       string
	ExpoFatalError        string
	RecentFailedUpdateIDs string
	XpremBranch           string
	// SurfBlockTokens is the xprem-surf-blocked header verbatim: the comma
	// separated "<branch>@<updateId>" verdicts the device echoes back, having
	// persisted them from a previous expo-server-defined-headers response.
	SurfBlockTokens string
}

type ManifestResult struct {
	Update     *types.Update
	BranchName string
	UpdateType types.UpdateType
	// BlockedSurf is set when the branch the device asked for was refused; the
	// response carries the verdict so the device stops asking for it.
	BlockedSurf *BlockedSurf
}

type ExpoProtocolError struct {
	StatusCode int
	Message    string
}

type AssetResolutionParams struct {
	RequestID      string
	AppID          string
	ChannelName    string
	AssetName      string
	RuntimeVersion string
	Platform       string
	// ClientID is the device's EAS-Client-ID header.
	ClientID string
	// Branch and UpdateID are the query params baked into manifest asset URLs; when
	// both are present and valid they pin the exact update the asset belongs to.
	Branch   string
	UpdateID string
	// RequestedUpdateID is the Expo-Requested-Update-ID header: the UUID of the
	// update the client is downloading.
	RequestedUpdateID string
}

type ExpoAssetError struct {
	StatusCode int
	Message    string
}

type ExpoAssetResult struct {
	RedirectToURL string
	Body          []byte
	ContentType   string
	Headers       map[string]string
	StatusCode    int
}

func (e *ExpoProtocolError) Error() string { return e.Message }

func (e *ExpoAssetError) Error() string { return e.Message }

func NewExpoProtocolService(appRepo AppRepository, channelRepo ChannelRepository, updateRepo UpdateRepository, updateService *UpdateService, branchRules []BranchRule) *ExpoProtocolService {
	return &ExpoProtocolService{
		appRepo:       appRepo,
		channelRepo:   channelRepo,
		updateRepo:    updateRepo,
		updateService: updateService,
		branchRules:   branchRules,
	}
}

func createMultipartResponse(headers map[string][]string, jsonContent interface{}) (*multipart.Writer, *bytes.Buffer, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	field, err := writer.CreatePart(headers)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating multipart field: %w", err)
	}
	contentJSON, err := json.Marshal(jsonContent)
	if err != nil {
		return nil, nil, fmt.Errorf("error marshaling JSON: %w", err)
	}
	if _, err := field.Write(contentJSON); err != nil {
		return nil, nil, fmt.Errorf("error writing JSON content: %w", err)
	}
	return writer, &buf, nil
}

func (s *ExpoProtocolService) signDirectiveOrManifest(ctx context.Context, appId string, content interface{}, expectSignatureHeader string) (string, error) {
	if expectSignatureHeader == "" {
		return "", nil
	}
	appConfig, err := s.cachedAppConfig(ctx, appId)
	if err != nil {
		return "", fmt.Errorf("failed to fetch app config for app ID '%s': %w", appId, err)
	}
	privateKey := keyStore.GetPrivateExpoKey(appConfig)
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("error stringifying content: %w", err)
	}
	// The key fingerprint is part of the cache key so a key rotation misses
	// the cache instead of serving a signature made with the old key.
	keyFingerprint, err := crypto.CreateHash([]byte(privateKey), "sha256", "hex")
	if err != nil {
		return "", fmt.Errorf("error hashing signing key fingerprint: %w", err)
	}
	contentHash, err := crypto.CreateHash(contentJSON, "sha256", "hex")
	if err != nil {
		return "", fmt.Errorf("error hashing signed content: %w", err)
	}
	cacheKey := signatureCacheKey(appId, keyFingerprint, contentHash)
	signatureCache := cache2.GetCache()
	if signedHash := signatureCache.Get(cacheKey); signedHash != "" {
		return signedHash, nil
	}
	signedHash, err := crypto.SignRSASHA256(string(contentJSON), privateKey)
	if err != nil {
		return "", fmt.Errorf("error signing content hash: %w", err)
	}
	ttl := signatureCacheTTLSeconds
	_ = signatureCache.Set(cacheKey, signedHash, &ttl)
	return signedHash, nil
}

func writeResponse(w http.ResponseWriter, writer *multipart.Writer, buf *bytes.Buffer, protocolVersion int64, runtimeVersion string, requestID string) {
	w.Header().Set("expo-protocol-version", strconv.FormatInt(protocolVersion, 10))
	w.Header().Set("expo-sfv-version", "0")
	w.Header().Set("cache-control", "private, max-age=0")
	w.Header().Set("content-type", "multipart/mixed; boundary="+writer.Boundary())
	if err := writer.Close(); err != nil {
		log.Printf("[RequestID: %s] Error closing multipart writer: %v", requestID, err)
		http.Error(w, "Error closing multipart writer", http.StatusInternalServerError)
		return
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		log.Printf("[RequestID: %s] Error writing response: %v", requestID, err)
	}
}

func (s *ExpoProtocolService) PutUpdateInResponse(w http.ResponseWriter, r *http.Request, appId string, lastUpdate types.Update, platform string, protocolVersion int64, requestID string, refusedBranch string) {
	currentUpdateId := r.Header.Get("expo-current-update-id")
	metadata, err := update2.GetMetadata(lastUpdate)
	if err != nil {
		log.Printf("[RequestID: %s] Error getting metadata: %v", requestID, err)
		http.Error(w, "Error getting metadata", http.StatusInternalServerError)
		return
	}

	if currentUpdateId != "" && currentUpdateId == crypto.ConvertSHA256HashToUUID(metadata.ID) && protocolVersion == 1 {
		s.PutNoUpdateAvailableInResponse(w, r, appId, lastUpdate.RuntimeVersion, protocolVersion, requestID)
		return
	}
	manifest, err := update2.ComposeUpdateManifest(&metadata, lastUpdate, platform)
	if err != nil {
		log.Printf("[RequestID: %s] Error composing manifest: %v", requestID, err)
		http.Error(w, "Error composing manifest", http.StatusInternalServerError)
		return
	}
	// Stamped on the copy about to be served, never on the cached manifest: the
	// cache is keyed by branch, and this is per request. Signing happens
	// downstream, so it covers the stamp.
	manifest.Extra.BranchSurfingRefused = refusedBranch
	if currentUpdateId != "" {
		metrics.TrackUpdateDownload(appId, platform, lastUpdate.RuntimeVersion, lastUpdate.Branch, manifest.Id, "update")
	}
	w.Header().Set("expo-manifest-filters", `branch="`+lastUpdate.Branch+`"`)
	s.PutResponse(w, r, appId, manifest, "manifest", lastUpdate.RuntimeVersion, protocolVersion, requestID)
}

func (s *ExpoProtocolService) PutResponse(w http.ResponseWriter, r *http.Request, appId string, content interface{}, fieldName string, runtimeVersion string, protocolVersion int64, requestID string) {
	signedHash, err := s.signDirectiveOrManifest(r.Context(), appId, content, r.Header.Get("expo-expect-signature"))
	if err != nil {
		log.Printf("[RequestID: %s] Error signing content: %v", requestID, err)
		http.Error(w, "Error signing content", http.StatusInternalServerError)
		return
	}
	headers := map[string][]string{
		"Content-Disposition": {fmt.Sprintf("form-data; name=\"%s\"", fieldName)},
		"Content-Type":        {"application/json"},
		"content-type":        {"application/json; charset=utf-8"},
	}
	if signedHash != "" {
		headers["expo-signature"] = []string{fmt.Sprintf("sig=\"%s\", keyid=\"main\"", signedHash)}
	}
	writer, buf, err := createMultipartResponse(headers, content)
	if err != nil {
		log.Printf("[RequestID: %s] Error creating multipart response: %v", requestID, err)
		http.Error(w, "Error creating multipart response", http.StatusInternalServerError)
		return
	}
	writeResponse(w, writer, buf, protocolVersion, runtimeVersion, requestID)
}

func (s *ExpoProtocolService) PutRollbackInResponse(w http.ResponseWriter, r *http.Request, appId string, lastUpdate types.Update, platform string, protocolVersion int64, requestID string) {
	if protocolVersion == 0 {
		http.Error(w, "Rollback not supported in protocol version 0", http.StatusBadRequest)
		return
	}
	embeddedUpdateId := r.Header.Get("expo-embedded-update-id")
	if embeddedUpdateId == "" {
		http.Error(w, "No embedded update id provided", http.StatusBadRequest)
		return
	}
	currentUpdateId := r.Header.Get("expo-current-update-id")
	if currentUpdateId != "" && currentUpdateId == embeddedUpdateId {
		s.PutNoUpdateAvailableInResponse(w, r, appId, lastUpdate.RuntimeVersion, protocolVersion, requestID)
		return
	}

	// CreatedAt is nanoseconds since the epoch, not the milliseconds NormalizeTimestamp expects.
	commitTime := time.Unix(0, int64(lastUpdate.CreatedAt)).UTC().Format("2006-01-02T15:04:05.000Z")
	directive, err := update2.CreateRollbackDirective(lastUpdate, commitTime)
	if err != nil {
		log.Printf("[RequestID: %s] Error creating rollback directive: %v", requestID, err)
		http.Error(w, "Error creating rollback directive", http.StatusInternalServerError)
		return
	}
	metrics.TrackUpdateDownload(appId, platform, lastUpdate.RuntimeVersion, lastUpdate.Branch, lastUpdate.UpdateId, "rollback")
	s.PutResponse(w, r, appId, directive, "directive", lastUpdate.RuntimeVersion, protocolVersion, requestID)
}

func (s *ExpoProtocolService) PutNoUpdateAvailableInResponse(w http.ResponseWriter, r *http.Request, appId string, runtimeVersion string, protocolVersion int64, requestID string) {
	if protocolVersion == 0 {
		http.Error(w, "NoUpdateAvailable directive not available in protocol version 0", http.StatusNoContent)
		return
	}
	directive := update2.CreateNoUpdateAvailableDirective()
	s.PutResponse(w, r, appId, directive, "directive", runtimeVersion, protocolVersion, requestID)
}

func (s *ExpoProtocolService) ResolveManifestBundle(ctx context.Context, params ManifestRequestParams) (ManifestResult, error) {
	// [Stateless mode] Reject unknown app ids at the edge with a clean 404, otherwise
	// downstream services.FetchExpoChannelMapping → GetExpoAccessToken
	// returns an empty token for the unknown id and we end up POSTing to
	// api.expo.dev with `Bearer ` (no token), surfacing the upstream 401
	// as an opaque 500 to the client.
	if _, err := s.cachedAppConfig(ctx, params.AppID); err != nil {
		log.Printf("[RequestID: %s] Unknown app id %q", params.RequestID, params.AppID)
		return ManifestResult{}, &ExpoProtocolError{StatusCode: http.StatusNotFound, Message: "Unknown app id"}
	}

	branchMap, err := s.channelBranchMapping(ctx, params.AppID, params.ChannelName)
	if err != nil {
		log.Printf("[RequestID: %s] Error fetching channel mapping: %v", params.RequestID, err)
		return ManifestResult{}, &ExpoProtocolError{StatusCode: http.StatusInternalServerError, Message: fmt.Sprintf("Error fetching channel mapping: %v", err)}
	}
	if branchMap == nil {
		log.Printf("[RequestID: %s] No branch mapping found for channel: %s", params.RequestID, params.ChannelName)
		return ManifestResult{}, &ExpoProtocolError{StatusCode: http.StatusNotFound, Message: "No branch mapping found"}
	}

	servedBranch, lastUpdate, blockedSurfResult, err := s.resolveUpdateForDevice(ctx, params.RequestID, params.AppID, params.ChannelName, params.ClientID, params.Platform, params.RuntimeVersion, params.XpremBranch, params.SurfBlockTokens, params.RecentFailedUpdateIDs, branchMap)
	if err != nil {
		return ManifestResult{}, err
	}

	// Tracked AFTER resolution with the branch actually served: under a channel
	// rollout, attributing the in-bucket cohort to the default branch would make the
	// rollout invisible in the metrics it is meant to be judged from.
	if params.ExpoFatalError != "" {
		if params.CurrentUpdateID != "" {
			metrics.TrackUpdateErrorUsers(params.AppID, params.ClientID, params.Platform, params.RuntimeVersion, servedBranch, params.CurrentUpdateID)
		} else if params.RecentFailedUpdateIDs != "" {
			metrics.TrackUpdateErrorUsers(params.AppID, params.ClientID, params.Platform, params.RuntimeVersion, servedBranch, params.RecentFailedUpdateIDs)
		}
	}
	metrics.TrackActiveUser(params.AppID, params.ClientID, params.Platform, params.RuntimeVersion, servedBranch, params.CurrentUpdateID)

	if lastUpdate == nil {
		return ManifestResult{
			Update:      nil,
			BranchName:  servedBranch,
			BlockedSurf: blockedSurfResult,
		}, nil
	}
	updateType, err := s.cachedUpdateType(ctx, *lastUpdate)
	if err != nil {
		log.Printf("[RequestID: %s] Error determining update type: %v", params.RequestID, err)
		return ManifestResult{}, &ExpoProtocolError{StatusCode: http.StatusInternalServerError, Message: "Error determining update type"}
	}

	return ManifestResult{Update: lastUpdate, BranchName: servedBranch, UpdateType: updateType, BlockedSurf: blockedSurfResult}, nil
}

// resolveUpdateForDevice runs the branch rule chain, then serves the first candidate
// branch that resolves for the device. A branch "resolves" as soon as it has any
// checked update for (runtime version, platform), even when the per-device answer is
// nil (out-of-bucket with no control => noUpdateAvailable, deliberately no fallback to
// the next candidate). Shared by manifest and asset resolution so the two paths take
// the same rollout decision for a device.
func (s *ExpoProtocolService) resolveUpdateForDevice(ctx context.Context, requestID string, appId string, channelName string, clientID string, platform string, runtimeVersion string, requestedBranch string, surfBlockTokens string, failedUpdateIDsRaw string, branchMap *expo.ChannelResolution) (servedBranchName string, lastUpdate *types.Update, blocked *BlockedSurf, err error) {
	req := &BranchResolutionRequest{
		AppID:           appId,
		ChannelName:     channelName,
		ClientID:        clientID,
		Platform:        platform,
		RuntimeVersion:  runtimeVersion,
		Mapping:         branchMap,
		RequestedBranch: requestedBranch,
	}
	if requestedBranch != "" {
		enabled, pattern := s.branchSurfingEnabled(ctx, appId, channelName)
		req.Surfing = types.BranchSurfing{Enabled: enabled, Pattern: pattern}
	}
	candidates, err := ResolveBranchCandidates(ctx, s.branchRules, req)
	if err != nil {
		log.Printf("[RequestID: %s] Error resolving branch candidates: %v", requestID, err)
		return "", nil, nil, &ExpoProtocolError{StatusCode: http.StatusInternalServerError, Message: "Error resolving branch"}
	}
	// Gated on the rule having honoured the surf, not on the header being present:
	// a declined request is a plain poll, and refusing one would take a device off
	// the branch its own channel maps to. It also keeps the lookups below off the
	// steady-state path, and off every deployment where surfing is disabled.
	surfing := HonoursSurf(req)
	var blocks surfBlockSet
	if surfing && (surfBlockTokens != "" || failedUpdateIDsRaw != "") {
		blocks = s.collectSurfBlocks(ctx, appId, surfBlockTokens, failedUpdateIDsRaw)
	}

	servedBranch := branchMap.BranchName
	for _, candidate := range candidates {
		resolution, err := s.updateService.GetLatestUpdateForClient(ctx, appId, candidate, runtimeVersion, platform, clientID)
		if err != nil {
			log.Printf("[RequestID: %s] Error getting latest update: %v", requestID, err)
			return "", nil, nil, &ExpoProtocolError{StatusCode: http.StatusInternalServerError, Message: "Error getting latest update"}
		}
		if !resolution.BranchHasUpdate {
			continue
		}
		if surfing && candidate == requestedBranch && resolution.Update != nil &&
			blocks.contains(candidate, resolution.Update.UpdateId) {
			log.Printf("[RequestID: %s] Refusing surf to %q: update %s failed to launch on this device", requestID, candidate, resolution.Update.UpdateId)
			blocked = &BlockedSurf{BranchName: candidate, Token: surfBlockToken(candidate, resolution.Update.UpdateId)}
			continue
		}
		return candidate, resolution.Update, blocked, nil
	}
	return servedBranch, nil, blocked, nil
}

func (s *ExpoProtocolService) ResolveAssetBundle(ctx context.Context, params AssetResolutionParams) (*ExpoAssetResult, error) {
	// [Stateless mode] Same edge check as ManifestHandler, reject unknown ids with 404
	// rather than letting them flow into FetchExpoChannelMapping and
	// surfacing the upstream 401 as a 500.
	if _, err := s.cachedAppConfig(ctx, params.AppID); err != nil {
		log.Printf("[RequestID: %s] Unknown app id %q", params.RequestID, params.AppID)
		return &ExpoAssetResult{}, &ExpoProtocolError{StatusCode: http.StatusNotFound, Message: "Unknown app id"}
	}

	branchMap, err := s.channelBranchMapping(ctx, params.AppID, params.ChannelName)
	if err != nil {
		log.Printf("[RequestID: %s] Error fetching channel mapping: %v", params.RequestID, err)
		return &ExpoAssetResult{}, &ExpoProtocolError{StatusCode: http.StatusInternalServerError, Message: fmt.Sprintf("Error fetching channel mapping: %v", err)}
	}
	if branchMap == nil {
		log.Printf("[RequestID: %s] No branch mapping found for channel: %s", params.RequestID, params.ChannelName)
		return &ExpoAssetResult{}, &ExpoProtocolError{StatusCode: http.StatusNotFound, Message: "No branch mapping found"}
	}
	branchName, lastUpdate, err := s.resolveAssetUpdate(ctx, params, branchMap)
	if err != nil {
		return &ExpoAssetResult{}, err
	}

	req := assets.AssetsRequest{
		AppId:          params.AppID,
		Branch:         branchName,
		AssetName:      params.AssetName,
		RuntimeVersion: params.RuntimeVersion,
		Platform:       params.Platform,
		Update:         lastUpdate,
		RequestID:      params.RequestID,
	}

	cdn := cdn2.GetCDN()

	if cdn == nil {
		resp, err := assets.HandleAssetsWithFile(req)
		if err != nil {
			return nil, &ExpoAssetError{StatusCode: http.StatusInternalServerError, Message: "Internal Server Error"}
		}

		return &ExpoAssetResult{
			Body:        resp.Body,
			ContentType: resp.ContentType,
			Headers:     resp.Headers,
			StatusCode:  resp.StatusCode,
		}, nil
	}

	resp, err := assets.HandleAssetsWithURL(req, cdn)
	if err != nil {
		return nil, &ExpoAssetError{StatusCode: http.StatusInternalServerError, Message: "Internal Server Error"}
	}

	// A miss (no update yet for this runtime version, unknown asset) comes back
	// as a non-200 StatusCode with a nil error and an empty URL. Keeping only
	// the URL would hand the handler a zero StatusCode and no redirect target,
	// which lands in http.Error(w, "", 0) and panics in WriteHeader.
	if resp.StatusCode != http.StatusOK {
		return &ExpoAssetResult{
			Body:        resp.Body,
			ContentType: resp.ContentType,
			Headers:     resp.Headers,
			StatusCode:  resp.StatusCode,
		}, nil
	}

	return &ExpoAssetResult{
		RedirectToURL: resp.URL,
	}, nil
}

// resolveAssetUpdate picks the update an asset request is served from, in three tiers:
//
//  1. The updateId/branch query params baked into manifest asset URLs, validated
//     app-scoped and against the channel's default and rollout branches.
//  2. The Expo-Requested-Update-ID header (the UUID of the update the client is
//     downloading), resolved app-scoped over checked updates only and held to the
//     same channel branch restriction as tier 1. A device caught mid-download by a
//     cross-branch channel repoint falls through to tier 3 (the pre-header behavior)
//     rather than the header working as a cross-branch read primitive.
//  3. The same rule-engine decision as /manifest, so a client that carries neither
//     hint still lands on the update its manifest resolution chose.
//
// Tiers 1 and 2 only exist on the control plane; in stateless mode resolution goes
// straight to tier 3, which with no rollout state degrades to exactly today's
// latest-update behavior.
func (s *ExpoProtocolService) resolveAssetUpdate(ctx context.Context, params AssetResolutionParams, branchMap *expo.ChannelResolution) (string, *types.Update, error) {
	if config.IsDBMode() {
		if params.UpdateID != "" && params.Branch != "" && s.isAssetBranchAllowed(ctx, params.AppID, params.ChannelName, params.Branch, branchMap) {
			pinnedUpdate, err := s.updateRepo.GetUpdate(ctx, params.AppID, params.Branch, params.RuntimeVersion, params.UpdateID)
			if err != nil {
				log.Printf("[RequestID: %s] Ignoring invalid updateId param %q: %v", params.RequestID, params.UpdateID, err)
			} else if pinnedUpdate != nil {
				valid, err := s.updateRepo.IsUpdateValid(ctx, *pinnedUpdate)
				if err != nil {
					log.Printf("[RequestID: %s] Error checking update validity: %v", params.RequestID, err)
					return "", nil, &ExpoProtocolError{StatusCode: http.StatusInternalServerError, Message: "Error getting latest update"}
				}
				if valid {
					return params.Branch, pinnedUpdate, nil
				}
			}
		}
		if params.RequestedUpdateID != "" {
			requestedUpdate, err := s.updateRepo.GetUpdateByUUID(ctx, params.AppID, params.RequestedUpdateID)
			if err != nil {
				log.Printf("[RequestID: %s] Ignoring invalid Expo-Requested-Update-ID %q: %v", params.RequestID, params.RequestedUpdateID, err)
			} else if requestedUpdate != nil {
				if s.isAssetBranchAllowed(ctx, params.AppID, params.ChannelName, requestedUpdate.Branch, branchMap) {
					return requestedUpdate.Branch, requestedUpdate, nil
				}
				log.Printf("[RequestID: %s] Ignoring Expo-Requested-Update-ID %q: branch %q is not served by channel %q", params.RequestID, params.RequestedUpdateID, requestedUpdate.Branch, params.ChannelName)
			}
		}
	}
	branchName, lastUpdate, _, err := s.resolveUpdateForDevice(ctx, params.RequestID, params.AppID, params.ChannelName, params.ClientID, params.Platform, params.RuntimeVersion, params.Branch, "", "", branchMap)
	return branchName, lastUpdate, err
}

// isAssetBranchAllowed answers the mirror of the manifest question: could manifest
// resolution for this channel have served this branch? It must stay exactly as
// permissive as that resolution. Wider and the branch query param becomes a
// cross-branch read primitive; narrower and the assets of a legitimately surfed
// branch 404.
func (s *ExpoProtocolService) isAssetBranchAllowed(ctx context.Context, appId string, channelName string, branchName string, branchMap *expo.ChannelResolution) bool {
	if branchName == branchMap.BranchName {
		return true
	}
	if branchMap.Rollout != nil && branchName == branchMap.Rollout.BranchName {
		return true
	}
	enabled, pattern := s.branchSurfingEnabled(ctx, appId, channelName)
	return enabled && branch.MatchPattern(pattern, branchName)
}
