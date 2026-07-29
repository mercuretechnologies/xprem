package handlers

import (
	"encoding/json"
	"errors"
	"expo-open-ota/internal/services"
	types2 "expo-open-ota/internal/types"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type RepublishHandler struct {
	deploymentService *services.DeploymentService
}

func NewRepublishHandler(deploymentService *services.DeploymentService) *RepublishHandler {
	return &RepublishHandler{
		deploymentService: deploymentService,
	}
}

// HandleRepublish re-publishes previous updates, in one of two modes:
// ?updateId=<id>&platform=ios|android republishes that single update
// (historical behavior), ?publishGroup=<uuid> republishes every member of that
// publish group on its own platform, the new rows sharing a new server-minted
// group returned in the response.
func (h *RepublishHandler) HandleRepublish(w http.ResponseWriter, r *http.Request) {
	requestID := uuid.New().String()
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	platform := r.URL.Query().Get("platform")
	publishGroup, err := parsePublishGroupTarget(r)
	if err != nil {
		log.Printf("[RequestID: %s] %v", requestID, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if publishGroup != nil && (platform != "" || r.URL.Query().Get("updateId") != "") {
		log.Printf("[RequestID: %s] Both updateId/platform and publishGroup provided", requestID)
		http.Error(w, "Provide either an updateId and platform or a publishGroup, not both", http.StatusBadRequest)
		return
	}
	if publishGroup == nil && (platform == "" || (platform != "ios" && platform != "android")) {
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
	commitHash := r.URL.Query().Get("commitHash")

	if publishGroup != nil {
		result, err := h.deploymentService.RepublishPublishGroup(r.Context(), appId, branchName, runtimeVersion, *publishGroup)
		if err != nil {
			if errors.Is(err, services.ErrActiveRolloutBlocksPublish) {
				log.Printf("[RequestID: %s] Group republish blocked by active rollout: %v", requestID, err)
				http.Error(w, activeRolloutConflictMessage, http.StatusConflict)
				return
			}
			if errors.Is(err, services.ErrPublishGroupNotFound) {
				log.Printf("[RequestID: %s] Publish group %s not found", requestID, *publishGroup)
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			var rErr *services.RepublishError
			if errors.As(err, &rErr) {
				log.Printf("[RequestID: %s] Group republish error: %v", requestID, err)
				http.Error(w, err.Error(), rErr.Status)
				return
			}
			log.Printf("[RequestID: %s] Unexpected error during group republish: %v", requestID, err)
			http.Error(w, "An unexpected error occurred during republish", http.StatusInternalServerError)
			return
		}
		log.Printf("[RequestID: %s] Publish group %s republished as group %s (%d platforms)", requestID, *publishGroup, result.PublishGroup, len(result.Updates))
		// The new group of the created rows; its presence is also how the CLI
		// knows the server understood the group semantics.
		w.Header().Set("expo-publish-group", result.PublishGroup)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"publishGroup": result.PublishGroup,
			"updates":      result.Updates,
		})
		return
	}

	updateId := r.URL.Query().Get("updateId")
	if updateId == "" {
		log.Printf("[RequestID: %s] No updateId provided", requestID)
		http.Error(w, "No updateId provided", http.StatusBadRequest)
		return
	}
	previousUpdate := &types2.Update{
		AppId:          appId,
		Branch:         branchName,
		RuntimeVersion: runtimeVersion,
		UpdateId:       updateId,
	}
	newUpdate, err := h.deploymentService.RepublishUpdate(r.Context(), previousUpdate, platform, commitHash, nil)
	if err != nil {
		if errors.Is(err, services.ErrActiveRolloutBlocksPublish) {
			log.Printf("[RequestID: %s] Republish blocked by active rollout: %v", requestID, err)
			http.Error(w, activeRolloutConflictMessage, http.StatusConflict)
			return
		}
		var rErr *services.RepublishError
		if errors.As(err, &rErr) {
			log.Printf("[RequestID: %s] Republish error: %v", requestID, rErr)
			http.Error(w, rErr.Message, rErr.Status)
			return
		}
		log.Printf("[RequestID: %s] Unexpected error during republish: %v", requestID, err)
		http.Error(w, "An unexpected error occurred during republish", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(newUpdate)
}
