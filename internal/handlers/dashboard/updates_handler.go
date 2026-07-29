package handlers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	cache2 "xprem/internal/cache"
	"xprem/internal/dashboard"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

const (
	defaultUpdateFeedLimit = 50
	maxUpdateFeedLimit     = 100
	// maxPublishBodyBytes caps the two publish bodies below. They hold a
	// platform, an id and a short message, so anything larger is a mistake or
	// an attempt to make the server buffer a request body.
	maxPublishBodyBytes = 4 << 10
)

type updateFeedCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	BranchID  int64     `json:"branchId"`
	UpdateID  int64     `json:"updateId"`
}

func parseUpdateFeedDate(raw string, endOfDay bool) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return &parsed, nil
	}
	parsed, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, err
	}
	if endOfDay {
		parsed = parsed.Add(24*time.Hour - time.Nanosecond)
	}
	return &parsed, nil
}

func decodeUpdateFeedCursor(raw string) (*updateFeedCursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var cursor updateFeedCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		return nil, err
	}
	return &cursor, nil
}

func encodeUpdateFeedCursor(item types.UpdateFeedItem) string {
	encoded, _ := json.Marshal(updateFeedCursor{
		CreatedAt: item.FeedCreatedAt,
		BranchID:  item.BranchID,
		UpdateID:  mustParseUpdateID(item.UpdateId),
	})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func mustParseUpdateID(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

type UpdateHandler struct {
	updateService     *services.UpdateService
	deploymentService *services.DeploymentService
}

func NewUpdateHandler(updateService *services.UpdateService, deploymentService *services.DeploymentService) *UpdateHandler {
	return &UpdateHandler{
		updateService:     updateService,
		deploymentService: deploymentService,
	}
}

func (h *UpdateHandler) GetUpdateDetailsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	runtimeVersion := vars["RUNTIME_VERSION"]
	updateId := vars["UPDATE_ID"]
	if branchName == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Branch name is empty")
		return
	}
	if runtimeVersion == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Runtime version is empty")
		return
	}
	if updateId == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Update ID is empty")
		return
	}
	cacheKey := dashboard.ComputeGetUpdateDetailsCacheKey(appId, branchName, runtimeVersion, updateId)
	cache := cache2.GetCache()
	if cacheValue := cache.Get(cacheKey); cacheValue != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(cacheValue))
		return
	}
	update, err := h.updateService.GetUpdateDetails(r.Context(), appId, branchName, runtimeVersion, updateId)
	if err != nil {
		var valErr *validation.Error
		if errors.As(err, &valErr) {
			handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
			return
		}
		handlers.RenderError(w, http.StatusBadRequest, "An internal error occurred while fetching update details.")
		return
	}
	updatesResponse := types.UpdateDetails{
		UpdateUUID:        update.UpdateUUID,
		UpdateId:          update.UpdateId,
		CreatedAt:         update.CreatedAt,
		CommitHash:        update.CommitHash,
		Platform:          update.Platform,
		Message:           update.Message,
		Type:              update.Type,
		ExpoConfig:        update.ExpoConfig,
		RolloutPercentage: update.RolloutPercentage,
		ControlUpdateId:   update.ControlUpdateId,
	}
	marshaledResponse, _ := json.Marshal(updatesResponse)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)

	ttl := 604800 // 7 days
	cache.Set(cacheKey, string(marshaledResponse), &ttl)
}

func (h *UpdateHandler) GetUpdatesHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	runtimeVersion := vars["RUNTIME_VERSION"]
	if branchName == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Branch name is empty")
		return
	}
	if runtimeVersion == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Runtime version is empty")
		return
	}
	cacheKey := dashboard.ComputeGetUpdatesCacheKey(appId, branchName, runtimeVersion)
	cache := cache2.GetCache()
	if cacheValue := cache.Get(cacheKey); cacheValue != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(cacheValue))
		return
	}
	updates, err := h.updateService.GetUpdatesByRunTimeVersionAndBranchName(r.Context(), appId, runtimeVersion, branchName)
	if err != nil {
		var valErr *validation.Error
		if errors.As(err, &valErr) {
			handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while fetching updates.")
		return
	}
	marshaledResponse, _ := json.Marshal(updates)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)

	ttl := 3600
	cache.Set(cacheKey, string(marshaledResponse), &ttl)
}

// publishResponse is the shared body of both write routes below: the rows that
// were created, in the order they were created (one per platform acted on).
// PublishGroup is set only by a group republish, which mints one for the rows
// it creates.
type publishResponse struct {
	Updates      []types.Update `json:"updates"`
	PublishGroup string         `json:"publishGroup,omitempty"`
}

// renderPublishError maps the deployment service errors onto the RFC 7807
// responses the dashboard expects. Shared by the rollback and republish routes:
// both go through the same publish path and can fail the same ways.
func renderPublishError(w http.ResponseWriter, err error, fallbackDetail string) {
	if errors.Is(err, services.ErrActiveRolloutBlocksPublish) {
		handlers.RenderError(w, http.StatusConflict, "A progressive rollout is active on this branch and runtime version. Finish or revert it first.")
		return
	}
	if errors.Is(err, services.ErrRolloutSuperseded) {
		handlers.RenderError(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, services.ErrPublishGroupNotFound) {
		handlers.RenderError(w, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, store.ErrNotSupportedInStatelessMode) {
		handlers.RenderError(w, http.StatusBadRequest, "Publish groups require the database control plane. Republish the update by id instead.")
		return
	}
	var republishErr *services.RepublishError
	if errors.As(err, &republishErr) {
		handlers.RenderError(w, republishErr.Status, republishErr.Message)
		return
	}
	var valErr *validation.Error
	if errors.As(err, &valErr) {
		handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
		return
	}
	handlers.RenderError(w, http.StatusInternalServerError, fallbackDetail)
}

// validateBranchAndRuntime rejects the two path segments before they reach the
// stores. Both end up as bucket path segments in stateless mode, so this is the
// same gate the read routes get through UpdateService.
func validateBranchAndRuntime(w http.ResponseWriter, branchName string, runtimeVersion string) bool {
	if err := validation.Name("branchName", branchName); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if err := validation.Name("runtimeVersion", runtimeVersion); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// CreateRollbackHandler sends a branch and runtime version back to the bundle
// embedded in the binary. It is the dashboard half of the CLI's rollback
// command (internal/handlers/rollback_handler.go), with one difference: the
// platform is optional here and empty means both, because the dashboard has no
// per-platform invocation to repeat.
func (h *UpdateHandler) CreateRollbackHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	runtimeVersion := vars["RUNTIME_VERSION"]
	if !validateBranchAndRuntime(w, branchName, runtimeVersion) {
		return
	}

	var body struct {
		// Empty means both platforms.
		Platform string `json:"platform"`
		Message  string `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPublishBodyBytes)).Decode(&body); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Platform != "" && body.Platform != "ios" && body.Platform != "android" {
		handlers.RenderError(w, http.StatusBadRequest, "platform must be \"ios\", \"android\", or empty for every platform")
		return
	}
	// Required, unlike the CLI's: a rollback from the dashboard is the one
	// publish nobody can trace back to a commit, so the row has to say why.
	message := strings.TrimSpace(body.Message)
	if err := validation.DisplayName("message", message); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Same fan-out as running the CLI's rollback command once per platform,
	// which is what an operator without a working copy would otherwise do.
	targets := []string{"ios", "android"}
	if body.Platform != "" {
		targets = []string{body.Platform}
	}

	// One row per platform, and no transaction spans them: the first failure
	// stops the run, the same partial-completion contract as a per-platform CLI
	// loop. A failure with rows already created reports the state it left
	// behind rather than the reason it stopped, because that state is what the
	// caller has to act on and it is not what any of the mapped statuses says.
	created := make([]types.Update, 0, len(targets))
	for _, platform := range targets {
		rollback, err := h.deploymentService.CreateRollback(r.Context(), appId, platform, "", runtimeVersion, branchName, message)
		if err != nil {
			if len(created) > 0 {
				log.Printf("Partial rollback on %s/%s: %s done, %s failed: %v", branchName, runtimeVersion, strings.Join(targets[:len(created)], ", "), platform, err)
				handlers.RenderError(w, http.StatusConflict, fmt.Sprintf(
					"The rollback was created for %s but failed for %s. Retry it for %s alone.",
					strings.Join(targets[:len(created)], ", "), platform, platform))
				return
			}
			renderPublishError(w, err, "An internal error occurred while creating the rollback.")
			return
		}
		created = append(created, *rollback)
	}
	handlers.RenderJSON(w, http.StatusCreated, publishResponse{Updates: created})
}

// RepublishUpdateHandler puts a past update back at the head of its branch, in
// one of two modes: {"updateId": "..."} republishes that single update, and
// {"publishGroup": "<uuid>"} republishes every per-platform member of one
// publish group at once. The group mode needs the control plane (stateless mode
// does not group rows); the single mode works on both.
//
// The new rows are clones of the source, so neither the platform nor the commit
// hash is taken from the request: see DeploymentService.RepublishUpdateByID.
func (h *UpdateHandler) RepublishUpdateHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	runtimeVersion := vars["RUNTIME_VERSION"]
	if !validateBranchAndRuntime(w, branchName, runtimeVersion) {
		return
	}

	var body struct {
		UpdateId     string `json:"updateId"`
		PublishGroup string `json:"publishGroup"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPublishBodyBytes)).Decode(&body); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if (body.UpdateId == "") == (body.PublishGroup == "") {
		handlers.RenderError(w, http.StatusBadRequest, "Provide either an updateId or a publishGroup, not both")
		return
	}

	if body.PublishGroup != "" {
		if _, err := uuid.Parse(body.PublishGroup); err != nil {
			handlers.RenderError(w, http.StatusBadRequest, "publishGroup must be a UUID")
			return
		}
		result, err := h.deploymentService.RepublishPublishGroup(r.Context(), appId, branchName, runtimeVersion, body.PublishGroup)
		if err != nil {
			renderPublishError(w, err, "An internal error occurred while republishing the publish group.")
			return
		}
		handlers.RenderJSON(w, http.StatusCreated, publishResponse{
			Updates:      result.Updates,
			PublishGroup: result.PublishGroup,
		})
		return
	}

	if err := validation.NumericID("updateId", body.UpdateId); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
		return
	}
	newUpdate, err := h.deploymentService.RepublishUpdateByID(r.Context(), appId, branchName, runtimeVersion, body.UpdateId)
	if err != nil {
		renderPublishError(w, err, "An internal error occurred while republishing the update.")
		return
	}
	handlers.RenderJSON(w, http.StatusCreated, publishResponse{Updates: []types.Update{*newUpdate}})
}

func (h *UpdateHandler) GetUpdateFeedHandler(w http.ResponseWriter, r *http.Request) {
	appId := mux.Vars(r)["APP_ID"]
	params := r.URL.Query()
	limit := defaultUpdateFeedLimit
	if rawLimit := params.Get("limit"); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > maxUpdateFeedLimit {
			handlers.RenderError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	from, err := parseUpdateFeedDate(params.Get("from"), false)
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "from must be an RFC3339 timestamp or YYYY-MM-DD date")
		return
	}
	to, err := parseUpdateFeedDate(params.Get("to"), true)
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "to must be an RFC3339 timestamp or YYYY-MM-DD date")
		return
	}
	cursor, err := decodeUpdateFeedCursor(params.Get("cursor"))
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "cursor is invalid")
		return
	}
	query := types.UpdateFeedQuery{
		Branch:         params.Get("branch"),
		RuntimeVersion: params.Get("runtimeVersion"),
		Platform:       params.Get("platform"),
		UpdateUUID:     params.Get("uuid"),
		PublishGroup:   params.Get("groupId"),
		CommitHash:     params.Get("commitHash"),
		From:           from,
		To:             to,
		Limit:          limit + 1,
	}
	if cursor != nil {
		query.CursorCreatedAt = &cursor.CreatedAt
		query.CursorBranchID = cursor.BranchID
		query.CursorUpdateID = cursor.UpdateID
	}
	updates, err := h.updateService.GetUpdateFeed(r.Context(), appId, query)
	if err != nil {
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while fetching the update feed.")
		return
	}
	if updates == nil {
		updates = []types.UpdateFeedItem{}
	}
	page := types.UpdateFeedPage{Items: updates}
	if len(updates) > limit {
		page.Items = updates[:limit]
		page.NextCursor = encodeUpdateFeedCursor(page.Items[len(page.Items)-1])
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(page)
}
