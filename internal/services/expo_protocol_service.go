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
	"strings"
	"time"
	"xprem/config"
	"xprem/internal/assets"
	"xprem/internal/branch"
	"xprem/internal/bucket"
	cache2 "xprem/internal/cache"
	cdn2 "xprem/internal/cdn"
	"xprem/internal/crypto"
	"xprem/internal/keyStore"
	"xprem/internal/metrics"
	"xprem/internal/types"
	update2 "xprem/internal/update"

	"github.com/google/uuid"
)

type ExpoProtocolService struct {
	appRepo       AppRepository
	channelRepo   ChannelRepository
	updateRepo    UpdateRepository
	updateService *UpdateService
	branchRules   []BranchRule
	bucket        bucket.Bucket
}

type ManifestRequestParams struct {
	RequestID             string
	AppID                 string
	ChannelName           string
	Platform              types.Platform
	RuntimeVersion        string
	ProtocolVersion       int64
	ClientID              string
	CurrentUpdateID       string
	EmbeddedUpdateID      string
	ExpectSignature       string
	ExpoFatalError        string
	RecentFailedUpdateIDs string
	XpremBranch           string
	// SurfBlockTokens is the xprem-surf-blocked header verbatim: the comma
	// separated "<branch>@<updateId>" verdicts the device echoes back, having
	// persisted them from a previous expo-server-defined-headers response.
	SurfBlockTokens string
}

type UpdateDecision struct {
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
	Platform       types.Platform
	// ClientID is the device's EAS-Client-ID header.
	ClientID string
	// Branch and UpdateID are the query params baked into manifest asset URLs; when
	// both are present and valid they pin the exact update the asset belongs to.
	Branch   string
	UpdateID string
	// RequestedUpdateID is the Expo-Requested-Update-ID header: the UUID of the
	// update the client is downloading.
	RequestedUpdateID string
	// Hash and Extension are the content-addressed form of an asset URL, baked
	// into the manifests of updates whose files live in cas/. The hash is the
	// whole address: no branch, runtime version or update pins it.
	Hash                string
	Extension           string
	ExpoCurrentUpdateId string
	AIM                 string
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
	// Uncompressed bodies are written as is, whatever Accept-Encoding says.
	Uncompressed bool
}

func (e *ExpoProtocolError) Error() string { return e.Message }

func (e *ExpoAssetError) Error() string { return e.Message }

func NewExpoProtocolService(appRepo AppRepository, channelRepo ChannelRepository, updateRepo UpdateRepository, updateService *UpdateService, branchRules []BranchRule, resolvedBucket bucket.Bucket) *ExpoProtocolService {
	return &ExpoProtocolService{
		appRepo:       appRepo,
		channelRepo:   channelRepo,
		updateRepo:    updateRepo,
		updateService: updateService,
		branchRules:   branchRules,
		bucket:        resolvedBucket,
	}
}

func createMultipartResponse(headers map[string][]string, contentJSON []byte) (*multipart.Writer, *bytes.Buffer, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	field, err := writer.CreatePart(headers)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating multipart field: %w", err)
	}
	if _, err := field.Write(contentJSON); err != nil {
		return nil, nil, fmt.Errorf("error writing JSON content: %w", err)
	}
	return writer, &buf, nil
}

// signContentBytes signs the exact bytes the response body carries, so the
// signature can never drift from what is served.
func (s *ExpoProtocolService) signContentBytes(ctx context.Context, appId string, contentJSON []byte, expectSignatureHeader string) (string, error) {
	if expectSignatureHeader == "" {
		return "", nil
	}
	appConfig, err := s.cachedAppConfig(ctx, appId)
	if err != nil {
		return "", fmt.Errorf("failed to fetch app config for app ID '%s': %w", appId, err)
	}
	privateKey := keyStore.GetPrivateExpoKey(appConfig)
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

func writeResponse(w http.ResponseWriter, writer *multipart.Writer, buf *bytes.Buffer, protocolVersion int64, requestID string) {
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

func (s *ExpoProtocolService) PutUpdateInResponse(ctx context.Context, w http.ResponseWriter, params ManifestRequestParams, lastUpdate types.Update, refusedBranch string) {
	// Envelope UUID first: the most common poll (device already up to date)
	// answers without reading the composed manifest. Empty for legacy rows
	// without a stored UUID; the entry comparison below still covers those.
	if params.CurrentUpdateID != "" && params.ProtocolVersion == 1 && lastUpdate.UpdateUUID != "" && params.CurrentUpdateID == lastUpdate.UpdateUUID {
		s.PutNoUpdateAvailableInResponse(ctx, w, params)
		return
	}
	entry, err := s.updateService.cachedManifestResponse(ctx, lastUpdate, params.Platform)
	if err != nil {
		log.Printf("[RequestID: %s] Error composing manifest: %v", params.RequestID, err)
		http.Error(w, "Error composing manifest", http.StatusInternalServerError)
		return
	}
	if params.CurrentUpdateID != "" && params.CurrentUpdateID == entry.UpdateUUID && params.ProtocolVersion == 1 {
		s.PutNoUpdateAvailableInResponse(ctx, w, params)
		return
	}
	contentJSON := []byte(entry.ManifestJSON)
	if refusedBranch != "" {
		// Per-request stamp on a copy, never on the cached bytes; the rebuilt
		// content gets its own signature entry by construction.
		var manifest types.UpdateManifest
		if err := json.Unmarshal(entry.ManifestJSON, &manifest); err != nil {
			log.Printf("[RequestID: %s] Error decoding cached manifest: %v", params.RequestID, err)
			http.Error(w, "Error composing manifest", http.StatusInternalServerError)
			return
		}
		manifest.Extra.BranchSurfingRefused = refusedBranch
		contentJSON, err = json.Marshal(manifest)
		if err != nil {
			log.Printf("[RequestID: %s] Error encoding manifest: %v", params.RequestID, err)
			http.Error(w, "Error composing manifest", http.StatusInternalServerError)
			return
		}
	}
	if params.CurrentUpdateID != "" {
		metrics.TrackUpdateDownload(params.AppID, string(params.Platform), lastUpdate.RuntimeVersion, lastUpdate.Branch, entry.UpdateUUID, "update")
	}
	w.Header().Set("expo-manifest-filters", `branch="`+lastUpdate.Branch+`"`)
	s.putRawResponse(ctx, w, params, contentJSON, "manifest")
}

func (s *ExpoProtocolService) PutResponse(ctx context.Context, w http.ResponseWriter, params ManifestRequestParams, content interface{}, fieldName string) {
	contentJSON, err := json.Marshal(content)
	if err != nil {
		log.Printf("[RequestID: %s] Error encoding content: %v", params.RequestID, err)
		http.Error(w, "Error encoding content", http.StatusInternalServerError)
		return
	}
	s.putRawResponse(ctx, w, params, contentJSON, fieldName)
}

func (s *ExpoProtocolService) putRawResponse(ctx context.Context, w http.ResponseWriter, params ManifestRequestParams, contentJSON []byte, fieldName string) {
	requestID := params.RequestID
	signedHash, err := s.signContentBytes(ctx, params.AppID, contentJSON, params.ExpectSignature)
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
	writer, buf, err := createMultipartResponse(headers, contentJSON)
	if err != nil {
		log.Printf("[RequestID: %s] Error creating multipart response: %v", requestID, err)
		http.Error(w, "Error creating multipart response", http.StatusInternalServerError)
		return
	}
	writeResponse(w, writer, buf, params.ProtocolVersion, requestID)
}

func (s *ExpoProtocolService) PutRollbackInResponse(ctx context.Context, w http.ResponseWriter, params ManifestRequestParams, lastUpdate types.Update) {
	requestID := params.RequestID
	if params.ProtocolVersion == 0 {
		http.Error(w, "Rollback not supported in protocol version 0", http.StatusBadRequest)
		return
	}
	if params.EmbeddedUpdateID == "" {
		http.Error(w, "No embedded update id provided", http.StatusBadRequest)
		return
	}
	if params.CurrentUpdateID != "" && params.CurrentUpdateID == params.EmbeddedUpdateID {
		s.PutNoUpdateAvailableInResponse(ctx, w, params)
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
	metrics.TrackUpdateDownload(params.AppID, string(params.Platform), lastUpdate.RuntimeVersion, lastUpdate.Branch, lastUpdate.UpdateId, "rollback")
	s.PutResponse(ctx, w, params, directive, "directive")
}

func (s *ExpoProtocolService) PutNoUpdateAvailableInResponse(ctx context.Context, w http.ResponseWriter, params ManifestRequestParams) {
	if params.ProtocolVersion == 0 {
		http.Error(w, "NoUpdateAvailable directive not available in protocol version 0", http.StatusNoContent)
		return
	}
	directive := update2.CreateNoUpdateAvailableDirective()
	s.PutResponse(ctx, w, params, directive, "directive")
}

func (s *ExpoProtocolService) ResolveUpdateForDevice(ctx context.Context, params ManifestRequestParams) (UpdateDecision, error) {
	if _, err := s.cachedAppConfig(ctx, params.AppID); err != nil {
		log.Printf("[RequestID: %s] Unknown app id %q", params.RequestID, params.AppID)
		return UpdateDecision{}, &ExpoProtocolError{StatusCode: http.StatusNotFound, Message: "Unknown app id"}
	}

	branchMap, err := s.channelBranchMapping(ctx, params.AppID, params.ChannelName)
	if err != nil {
		log.Printf("[RequestID: %s] Error fetching channel mapping: %v", params.RequestID, err)
		return UpdateDecision{}, &ExpoProtocolError{StatusCode: http.StatusInternalServerError, Message: fmt.Sprintf("Error fetching channel mapping: %v", err)}
	}
	if branchMap == nil {
		log.Printf("[RequestID: %s] No branch mapping found for channel: %s", params.RequestID, params.ChannelName)
		return UpdateDecision{}, &ExpoProtocolError{StatusCode: http.StatusNotFound, Message: "No branch mapping found"}
	}

	servedBranch, lastUpdate, blockedSurfResult, err := s.resolveUpdateAcrossBranches(ctx, params.RequestID, &BranchResolutionRequest{
		AppID:           params.AppID,
		ChannelName:     params.ChannelName,
		ClientID:        params.ClientID,
		Platform:        params.Platform,
		RuntimeVersion:  params.RuntimeVersion,
		Mapping:         branchMap,
		RequestedBranch: params.XpremBranch,
	}, params.SurfBlockTokens, params.RecentFailedUpdateIDs)
	if err != nil {
		return UpdateDecision{}, err
	}

	if params.ExpoFatalError != "" {
		if params.CurrentUpdateID != "" {
			metrics.TrackUpdateErrorUsers(params.AppID, params.ClientID, string(params.Platform), params.RuntimeVersion, servedBranch, params.CurrentUpdateID)
		} else if params.RecentFailedUpdateIDs != "" {
			metrics.TrackUpdateErrorUsers(params.AppID, params.ClientID, string(params.Platform), params.RuntimeVersion, servedBranch, params.RecentFailedUpdateIDs)
		}
	}
	metrics.TrackActiveUser(params.AppID, params.ClientID, string(params.Platform), params.RuntimeVersion, servedBranch, params.CurrentUpdateID)

	if lastUpdate == nil {
		return UpdateDecision{
			Update:      nil,
			BranchName:  servedBranch,
			BlockedSurf: blockedSurfResult,
		}, nil
	}
	updateType, err := s.cachedUpdateType(ctx, *lastUpdate)
	if err != nil {
		log.Printf("[RequestID: %s] Error determining update type: %v", params.RequestID, err)
		return UpdateDecision{}, &ExpoProtocolError{StatusCode: http.StatusInternalServerError, Message: "Error determining update type"}
	}

	return UpdateDecision{Update: lastUpdate, BranchName: servedBranch, UpdateType: updateType, BlockedSurf: blockedSurfResult}, nil
}

// resolveUpdateAcrossBranches runs the branch rule chain, then serves the first candidate
// branch that resolves for the device. A branch "resolves" as soon as it has any
// checked update for (runtime version, platform), even when the per-device answer is
// nil (out-of-bucket with no control => noUpdateAvailable, deliberately no fallback to
// the next candidate). Shared by manifest and asset resolution so the two paths take
// the same rollout decision for a device.
func (s *ExpoProtocolService) resolveUpdateAcrossBranches(ctx context.Context, requestID string, req *BranchResolutionRequest, surfBlockTokens string, failedUpdateIDsRaw string) (servedBranchName string, lastUpdate *types.Update, blocked *BlockedSurf, err error) {
	if req.RequestedBranch != "" {
		enabled, pattern := s.branchSurfingEnabled(ctx, req.AppID, req.ChannelName)
		req.Surfing = types.BranchSurfing{Enabled: enabled, Pattern: pattern}
	}
	candidates, err := ResolveBranchCandidates(ctx, s.branchRules, req)
	if err != nil {
		log.Printf("[RequestID: %s] Error resolving branch candidates: %v", requestID, err)
		return "", nil, nil, &ExpoProtocolError{StatusCode: http.StatusInternalServerError, Message: "Error resolving branch"}
	}
	surfing := HonoursSurf(req)
	var blocks surfBlockSet
	if surfing && (surfBlockTokens != "" || failedUpdateIDsRaw != "") {
		blocks = s.collectSurfBlocks(ctx, req.AppID, surfBlockTokens, failedUpdateIDsRaw)
	}

	servedBranch := req.Mapping.BranchName
	for _, candidate := range candidates {
		resolution, err := s.updateService.GetLatestUpdateForClient(ctx, req.AppID, candidate, req.RuntimeVersion, req.Platform, req.ClientID)
		if err != nil {
			log.Printf("[RequestID: %s] Error getting latest update: %v", requestID, err)
			return "", nil, nil, &ExpoProtocolError{StatusCode: http.StatusInternalServerError, Message: "Error getting latest update"}
		}
		if !resolution.BranchHasUpdate {
			continue
		}
		if surfing && candidate == req.RequestedBranch && resolution.Update != nil &&
			blocks.contains(candidate, resolution.Update.UpdateId) {
			log.Printf("[RequestID: %s] Refusing surf to %q: update %s failed to launch on this device", requestID, candidate, resolution.Update.UpdateId)
			blocked = &BlockedSurf{BranchName: candidate, Token: surfBlockToken(candidate, resolution.Update.UpdateId)}
			continue
		}
		return candidate, resolution.Update, blocked, nil
	}
	return servedBranch, nil, blocked, nil
}

// resolveBlobAsset serves an asset by its content hash. Nothing pins it to a
// branch or an update: the hash is unguessable and only ever reaches a client
// that already holds the manifest naming it. The app id still scopes the read,
// so one tenant cannot address another's blobs.
func (s *ExpoProtocolService) resolveBlobAsset(ctx context.Context, params AssetResolutionParams) (*ExpoAssetResult, error) {
	if cdn := cdn2.GetCDN(); cdn != nil {
		redirectURL, err := cdn.ComputeRedirectionURLForBlob(params.AppID, params.Hash)
		if err != nil {
			log.Printf("[RequestID: %s] Error signing blob url: %v", params.RequestID, err)
			return nil, &ExpoAssetError{StatusCode: http.StatusInternalServerError, Message: "Internal Server Error"}
		}
		return &ExpoAssetResult{RedirectToURL: redirectURL}, nil
	}

	blob, err := s.bucket.GetBlob(ctx, params.AppID, params.Hash)
	if err != nil {
		log.Printf("[RequestID: %s] Error reading blob: %v", params.RequestID, err)
		return nil, &ExpoAssetError{StatusCode: http.StatusInternalServerError, Message: "Internal Server Error"}
	}
	if blob == nil {
		return &ExpoAssetResult{}, &ExpoAssetError{StatusCode: http.StatusNotFound, Message: "Asset not found"}
	}
	body, err := bucket.ConvertReadCloserToBytes(blob.Reader)
	if err != nil {
		log.Printf("[RequestID: %s] Error reading blob body: %v", params.RequestID, err)
		return nil, &ExpoAssetError{StatusCode: http.StatusInternalServerError, Message: "Internal Server Error"}
	}
	return &ExpoAssetResult{
		Body: body,
		// "bundle" is the extension shapeAsset gives a launch asset, so the one
		// mime table answers for both kinds.
		ContentType: update2.AssetContentType(params.Extension, params.Extension == "bundle"),
		Headers:     assets.ExpoProtocolHeaders(),
		StatusCode:  http.StatusOK,
	}, nil
}

// resolveBSDiffAsset answers a launch asset request with the patch from the
// update the device runs to the one it downloads, or nil for the full bundle.
func (s *ExpoProtocolService) resolveBSDiffAsset(ctx context.Context, params AssetResolutionParams) *ExpoAssetResult {
	if !config.IsBundleDiffingEnabled() || !config.IsDBMode() || !acceptsBSDiff(params.AIM) {
		return nil
	}
	currentUUID, err := uuid.Parse(params.ExpoCurrentUpdateId)
	if err != nil {
		return nil
	}
	requestedUUID, err := uuid.Parse(params.RequestedUpdateID)
	if err != nil || requestedUUID == currentUUID {
		return nil
	}
	current, err := s.cachedUpdateByUUID(ctx, params.AppID, currentUUID.String())
	if err != nil {
		log.Printf("[RequestID: %s] Cannot resolve current update %s: %v", params.RequestID, currentUUID, err)
		return nil
	}
	requested, err := s.cachedUpdateByUUID(ctx, params.AppID, requestedUUID.String())
	if err != nil {
		log.Printf("[RequestID: %s] Cannot resolve requested update %s: %v", params.RequestID, requestedUUID, err)
		return nil
	}
	if current == nil || requested == nil || current.Branch != requested.Branch || current.RuntimeVersion != requested.RuntimeVersion {
		return nil
	}
	branch, target, source := requested.Branch, requestedUUID.String(), currentUUID.String()

	// A redirect to a missing object fails the device's download outright, so
	// the existence check is not optional on that path.
	exists, err := s.cachedPatchExists(ctx, params.AppID, branch, target, source)
	if err != nil {
		log.Printf("[RequestID: %s] Error checking patch %s -> %s: %v", params.RequestID, source, target, err)
		return nil
	}
	if !exists {
		return nil
	}
	if config.IsBundleDiffingCDNRedirect() {
		if cdn := cdn2.GetCDN(); cdn != nil {
			redirectURL, err := cdn.ComputeRedirectionURLForPatch(params.AppID, branch, target, source)
			if err == nil {
				return &ExpoAssetResult{RedirectToURL: redirectURL}
			}
			log.Printf("[RequestID: %s] Error signing patch url %s -> %s, serving it directly: %v", params.RequestID, source, target, err)
		}
	}

	patch, err := s.bucket.GetBSDiff(ctx, params.AppID, branch, target, source)
	if err != nil {
		log.Printf("[RequestID: %s] Error reading patch %s -> %s: %v", params.RequestID, source, target, err)
		return nil
	}
	if patch == nil {
		return nil
	}
	body, err := bucket.ConvertReadCloserToBytes(patch.Reader)
	if err != nil {
		log.Printf("[RequestID: %s] Error reading patch body %s -> %s: %v", params.RequestID, source, target, err)
		return nil
	}
	headers := assets.ExpoProtocolHeaders()
	headers["im"] = "bsdiff"
	headers["expo-base-update-id"] = currentUUID.String()
	headers["Cache-Control"] = "private, no-store"
	return &ExpoAssetResult{
		Body:         body,
		ContentType:  "application/octet-stream",
		Headers:      headers,
		StatusCode:   http.StatusOK,
		Uncompressed: true,
	}
}

func acceptsBSDiff(aim string) bool {
	for _, item := range strings.Split(aim, ",") {
		if strings.EqualFold(strings.TrimSpace(item), "bsdiff") {
			return true
		}
	}
	return false
}

func (s *ExpoProtocolService) ResolveAsset(ctx context.Context, params AssetResolutionParams) (*ExpoAssetResult, error) {
	if _, err := s.cachedAppConfig(ctx, params.AppID); err != nil {
		log.Printf("[RequestID: %s] Unknown app id %q", params.RequestID, params.AppID)
		return &ExpoAssetResult{}, &ExpoProtocolError{StatusCode: http.StatusNotFound, Message: "Unknown app id"}
	}

	if params.Hash != "" {
		if err := bucket.ValidateBlobHash(params.Hash); err != nil {
			log.Printf("[RequestID: %s] %v", params.RequestID, err)
			return &ExpoAssetResult{}, &ExpoAssetError{StatusCode: http.StatusBadRequest, Message: "Invalid asset hash"}
		}
		if patch := s.resolveBSDiffAsset(ctx, params); patch != nil {
			return patch, nil
		}
		return s.resolveBlobAsset(ctx, params)
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
func (s *ExpoProtocolService) resolveAssetUpdate(ctx context.Context, params AssetResolutionParams, branchMap *types.ChannelResolution) (string, *types.Update, error) {
	if config.IsDBMode() {
		if params.UpdateID != "" && params.Branch != "" && s.isAssetBranchAllowed(ctx, params.AppID, params.ChannelName, params.Branch, branchMap) {
			pinnedUpdate, err := s.updateRepo.GetCheckedUpdate(ctx, params.AppID, params.Branch, params.RuntimeVersion, params.UpdateID)
			if err != nil {
				log.Printf("[RequestID: %s] Ignoring invalid updateId param %q: %v", params.RequestID, params.UpdateID, err)
			} else if pinnedUpdate != nil {
				return params.Branch, pinnedUpdate, nil
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
	branchName, lastUpdate, _, err := s.resolveUpdateAcrossBranches(ctx, params.RequestID, &BranchResolutionRequest{
		AppID:           params.AppID,
		ChannelName:     params.ChannelName,
		ClientID:        params.ClientID,
		Platform:        params.Platform,
		RuntimeVersion:  params.RuntimeVersion,
		Mapping:         branchMap,
		RequestedBranch: params.Branch,
	}, "", "")
	return branchName, lastUpdate, err
}

// isAssetBranchAllowed answers the mirror of the manifest question: could manifest
// resolution for this channel have served this branch? It must stay exactly as
// permissive as that resolution. Wider and the branch query param becomes a
// cross-branch read primitive; narrower and the assets of a legitimately surfed
// branch 404.
func (s *ExpoProtocolService) isAssetBranchAllowed(ctx context.Context, appId string, channelName string, branchName string, branchMap *types.ChannelResolution) bool {
	if branchName == branchMap.BranchName {
		return true
	}
	if branchMap.Rollout != nil && branchName == branchMap.Rollout.BranchName {
		return true
	}
	enabled, pattern := s.branchSurfingEnabled(ctx, appId, channelName)
	return enabled && branch.MatchPattern(pattern, branchName)
}
