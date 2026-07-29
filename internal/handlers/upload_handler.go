package handlers

import (
	"encoding/json"
	"errors"
	"expo-open-ota/config"
	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/validation"
	"fmt"
	"log"
	"net/http"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type UploadHandler struct {
	cliAuthService    *services.CliAuthService
	deploymentService *services.DeploymentService
}

func NewUploadHandler(cliAuthService *services.CliAuthService, deploymentService *services.DeploymentService) *UploadHandler {
	return &UploadHandler{
		cliAuthService:    cliAuthService,
		deploymentService: deploymentService,
	}
}

type FileNamesRequest struct {
	FileNames []string `json:"fileNames"`
	Message   string   `json:"message,omitempty"`
}

// parsePublishGroup reads the optional CLI-minted id grouping the per-platform
// rows of one eoas publish run. In stateless mode the parameter is ignored
// entirely: the feature does not exist there, and the missing acknowledgment
// (echo or header) is how the CLI knows. In control-plane mode a malformed
// value is rejected so a buggy CLI cannot silently create ungrouped rows.
func parsePublishGroup(r *http.Request) (*string, error) {
	raw := r.URL.Query().Get("publishGroup")
	if raw == "" || !config.IsDBMode() {
		return nil, nil
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid publishGroup: must be a UUID")
	}
	normalized := parsed.String()
	return &normalized, nil
}

// parsePublishGroupTarget reads the publish group TARGETED by a group-wide
// republish. Unlike the stamp above, a target cannot be ignored in stateless
// mode: it selects what the operation acts on, so without the control plane
// it is rejected outright rather than silently degraded.
func parsePublishGroupTarget(r *http.Request) (*string, error) {
	raw := r.URL.Query().Get("publishGroup")
	if raw == "" {
		return nil, nil
	}
	if !config.IsDBMode() {
		return nil, fmt.Errorf("publish groups require the database control plane")
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid publishGroup: must be a UUID")
	}
	normalized := parsed.String()
	return &normalized, nil
}

func (h *UploadHandler) MarkUpdateAsUploadedHandler(w http.ResponseWriter, r *http.Request) {
	requestID := uuid.New().String()
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	platform := r.URL.Query().Get("platform")
	if platform == "" || (platform != "ios" && platform != "android") {
		log.Printf("[RequestID: %s] Invalid platform: %s", requestID, platform)
		http.Error(w, "Invalid platform", http.StatusBadRequest)
		return
	}
	if branchName == "" {
		log.Printf("[RequestID: %s] No branch provided", requestID)
		http.Error(w, "No branch provided", http.StatusBadRequest)
		return
	}
	runtimeVersion := r.URL.Query().Get("runtimeVersion")
	if runtimeVersion == "" {
		log.Printf("[RequestID: %s] No runtime version provided", requestID)
		http.Error(w, "No runtime version provided", http.StatusBadRequest)
		return
	}
	updateId := r.URL.Query().Get("updateId")
	if updateId == "" {
		log.Printf("[RequestID: %s] No update id provided", requestID)
		http.Error(w, "No update id provided", http.StatusBadRequest)
		return
	}
	params := services.ProcessUpdateParams{
		RequestID:      requestID,
		AppID:          appId,
		BranchName:     branchName,
		Platform:       platform,
		RuntimeVersion: runtimeVersion,
		UpdateID:       updateId,
	}
	err := h.deploymentService.ProcessUploadedUpdate(r.Context(), params)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			RenderCliAuthError(w, err)
			return
		}
		if errors.Is(err, services.ErrInvalidUpdate) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if errors.Is(err, services.ErrNoChangesDetected) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotAcceptable)

			response := map[string]string{
				"error": "You have already uploaded this update, no changes detected",
			}
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		if errors.Is(err, services.ErrActiveRolloutBlocksPublish) {
			log.Printf("[RequestID: %s] Mark-as-uploaded blocked by active rollout: %v", requestID, err)
			http.Error(w, activeRolloutConflictMessage, http.StatusConflict)
			return
		}
		if errors.Is(err, services.ErrRolloutSuperseded) {
			log.Printf("[RequestID: %s] Rollout activation superseded by newer update: %v", requestID, err)
			http.Error(w, services.ErrRolloutSuperseded.Error(), http.StatusConflict)
			return
		}

		// Any unexpected runtime/database systems error falls back to standard 500s
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *UploadHandler) RequestUploadLocalFileHandler(w http.ResponseWriter, r *http.Request) {
	requestID := uuid.New().String()
	appId := mux.Vars(r)["APP_ID"]

	token := r.URL.Query().Get("token")
	if token == "" {
		log.Printf("[RequestID: %s] No token provided", requestID)
		http.Error(w, "No token provided", http.StatusBadRequest)
		return
	}

	filePath, tokenAppId, _, err := bucket.ValidateUploadTokenAndResolveFilePath(token)
	if err != nil {
		log.Printf("[RequestID: %s] Error validating upload token: %v", requestID, err)
		http.Error(w, "Error validating upload token", http.StatusBadRequest)
		return
	}
	// No RequireAuthorizedBranch here, unlike the four handlers that take a
	// {BRANCH} from their path: this route names no branch, the router judged
	// the one this token claims, and ValidateUploadTokenAndResolveFilePath is
	// what guarantees filePath lies inside that same branch. Asserting the
	// branch claim against itself here would prove nothing.

	fileName := filepath.Base(filePath)
	file, _, err := r.FormFile(fileName)
	if err != nil {
		log.Printf("[RequestID: %s] Error retrieving file from form: %v", requestID, err)
		http.Error(w, "Error retrieving file from form", http.StatusBadRequest)
		return
	}
	defer file.Close()

	params := services.RequestLocalFileUploadParams{
		RequestID:  requestID,
		AppID:      appId,
		Token:      token,
		Body:       file,
		FilePath:   filePath,
		TokenAppID: tokenAppId,
	}

	if err := h.deploymentService.RequestUploadLocalFile(r.Context(), params); err != nil {
		if errors.Is(err, services.ErrInvalidBucketType) {
			http.Error(w, "Invalid bucket type", http.StatusInternalServerError)
			return
		}
		if errors.Is(err, services.ErrInvalidToken) {
			http.Error(w, "Error validating upload token", http.StatusBadRequest)
			return
		}
		if errors.Is(err, services.ErrTokenAppMismatch) {
			http.Error(w, "Upload token does not match this app", http.StatusForbidden)
			return
		}
		if errors.Is(err, services.ErrUploadFailed) {
			http.Error(w, "Error handling upload file", http.StatusInternalServerError)
			return
		}
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *UploadHandler) RequestUploadUrlHandler(w http.ResponseWriter, r *http.Request) {
	requestID := uuid.New().String()
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	if branchName == "" {
		log.Printf("[RequestID: %s] No branch provided", requestID)
		http.Error(w, "No branch provided", http.StatusBadRequest)
		return
	}


	platform := r.URL.Query().Get("platform")
	if platform != "" && (platform != "ios" && platform != "android") {
		log.Printf("[RequestID: %s] Invalid platform: %s", requestID, platform)
		http.Error(w, "Invalid platform", http.StatusBadRequest)
		return
	}
	commitHash := r.URL.Query().Get("commitHash")
	runtimeVersion := r.URL.Query().Get("runtimeVersion")
	if runtimeVersion == "" {
		log.Printf("[RequestID: %s] No runtime version provided", requestID)
		http.Error(w, "No runtime version provided", http.StatusBadRequest)
		return
	}

	var rolloutPercentage *int
	if rawRolloutPercentage := r.URL.Query().Get("rolloutPercentage"); rawRolloutPercentage != "" {
		parsedRolloutPercentage, err := validation.RolloutPercentage("rolloutPercentage", rawRolloutPercentage)
		if err != nil {
			log.Printf("[RequestID: %s] Invalid rolloutPercentage: %s", requestID, rawRolloutPercentage)
			http.Error(w, "Invalid "+err.Error(), http.StatusBadRequest)
			return
		}
		// 100 means every device, i.e. a plain publish: treated as absent.
		if parsedRolloutPercentage < 100 {
			if !config.IsDBMode() {
				log.Printf("[RequestID: %s] rolloutPercentage rejected in stateless mode", requestID)
				http.Error(w, "Progressive rollouts require the database control plane", http.StatusBadRequest)
				return
			}
			if platform == "" {
				log.Printf("[RequestID: %s] rolloutPercentage requires a platform", requestID)
				http.Error(w, "A platform is required when publishing with a rollout percentage", http.StatusBadRequest)
				return
			}
			rolloutPercentage = &parsedRolloutPercentage
		}
	}

	publishGroup, err := parsePublishGroup(r)
	if err != nil {
		log.Printf("[RequestID: %s] %v", requestID, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var bodyReq FileNamesRequest
	if err := json.NewDecoder(r.Body).Decode(&bodyReq); err != nil {
		log.Printf("[RequestID: %s] Error decoding JSON body: %v", requestID, err)
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if len(bodyReq.FileNames) == 0 {
		log.Printf("[RequestID: %s] No file names provided", requestID)
		http.Error(w, "No file names provided", http.StatusBadRequest)
		return
	}

	params := services.RequestUploadURLParams{
		RequestID:         requestID,
		AppID:             appId,
		BranchName:        branchName,
		Platform:          platform,
		CommitHash:        commitHash,
		RuntimeVersion:    runtimeVersion,
		FileNames:         bodyReq.FileNames,
		Message:           bodyReq.Message,
		RolloutPercentage: rolloutPercentage,
		PublishGroupID:    publishGroup,
	}

	result, err := h.deploymentService.RequestUploadURLs(r.Context(), params)
	if err != nil {
		if errors.Is(err, services.ErrActiveRolloutBlocksPublish) {
			log.Printf("[RequestID: %s] Publish blocked by active rollout: %v", requestID, err)
			http.Error(w, activeRolloutConflictMessage, http.StatusConflict)
			return
		}
		http.Error(w, "Internal server error processing payload URLs", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"updateId":       result.UpdateID,
		"uploadRequests": result.UploadRequests,
	}
	// Echoed back so the CLI can detect a server too old to know the parameter (an
	// old server silently ignores it and would publish to every device).
	if rolloutPercentage != nil {
		response["rolloutPercentage"] = *rolloutPercentage
	}
	// Same detection contract for grouping: no echo means the group was not
	// stored (old server or stateless mode) and the CLI warns instead of
	// pretending the rows are grouped.
	if publishGroup != nil {
		response["publishGroup"] = *publishGroup
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("expo-update-id", fmt.Sprintf("%d", result.UpdateID))
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[RequestID: %s] Error encoding response serialization: %v", requestID, err)
	}

}
