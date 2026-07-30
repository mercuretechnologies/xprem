package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"xprem/internal/services"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type RollbackHandler struct {
	deploymentService *services.DeploymentService
}

func NewRollbackHandler(deploymentService *services.DeploymentService) *RollbackHandler {
	return &RollbackHandler{
		deploymentService: deploymentService,
	}
}

func (h *RollbackHandler) HandleRollback(w http.ResponseWriter, r *http.Request) {
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
	commitHash := r.URL.Query().Get("commitHash")
	// No message: the CLI rollback command has no field for one.
	rollback, err := h.deploymentService.CreateRollback(r.Context(), appId, platform, commitHash, runtimeVersion, branchName, "")
	if err != nil {
		if errors.Is(err, services.ErrActiveRolloutBlocksPublish) {
			log.Printf("[RequestID: %s] Rollback blocked by active rollout: %v", requestID, err)
			http.Error(w, activeRolloutConflictMessage, http.StatusConflict)
			return
		}
		log.Printf("[RequestID: %s] Error creating rollback: %v", requestID, err)
		http.Error(w, "Error creating rollback", http.StatusInternalServerError)
		return
	}
	log.Printf("[RequestID: %s] Rollback created: %s", requestID, rollback.UpdateId)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(rollback)
}
