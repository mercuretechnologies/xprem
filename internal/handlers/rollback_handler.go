package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"xprem/internal/helpers"
	"xprem/internal/services"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type RollbackHandler struct {
	cliAuthService    *services.CliAuthService
	deploymentService *services.DeploymentService
}

func NewRollbackHandler(cliAuthService *services.CliAuthService, deploymentService *services.DeploymentService) *RollbackHandler {
	return &RollbackHandler{
		cliAuthService:    cliAuthService,
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
	auth := helpers.GetAuth(r)
	credential, err := h.cliAuthService.ValidateCliCredential(r.Context(), appId, auth, branchName, helpers.ClientIP(r))
	if err != nil {
		log.Printf("[RequestID: %s] Error validating auth: %v", requestID, err)
		RenderCliAuthError(w, err)
		return
	}
	r = r.WithContext(services.WithCliAuth(r.Context(), credential))
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
