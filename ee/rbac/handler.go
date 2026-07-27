// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"encoding/json"
	"errors"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/store"
	"net/http"
	"time"

	"github.com/gorilla/mux"
)

// RBACHandler serves the management API: role CRUD and per-user grants
// (admin-only routes), plus the current account's permission map the
// dashboard gates its UI with.
type RBACHandler struct {
	service *RBACService
}

func NewRBACHandler(service *RBACService) *RBACHandler {
	return &RBACHandler{service: service}
}

type RoleResponse struct {
	Id          string   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	CreatedAt   string   `json:"createdAt,omitempty"`
	UpdatedAt   string   `json:"updatedAt,omitempty"`
}

func roleResponseFrom(role Role) RoleResponse {
	response := RoleResponse{
		Id:          role.ID,
		Name:        role.Name,
		Permissions: fromPermissions(role.Permissions),
	}
	if !role.CreatedAt.IsZero() {
		response.CreatedAt = role.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !role.UpdatedAt.IsZero() {
		response.UpdatedAt = role.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return response
}

// GrantResponse is one row of a member's access list. EffectivePermissions is
// precomputed server-side (role ∪ extras) so every consumer displays the same
// truth the enforcement uses.
type GrantResponse struct {
	AppId                string   `json:"appId"`
	RoleId               *string  `json:"roleId"`
	RoleName             *string  `json:"roleName"`
	ExtraPermissions     []string `json:"extraPermissions"`
	EffectivePermissions []string `json:"effectivePermissions"`
}

func grantResponseFrom(grant AppGrant) GrantResponse {
	return GrantResponse{
		AppId:                grant.AppID,
		RoleId:               grant.RoleID,
		RoleName:             grant.RoleName,
		ExtraPermissions:     fromPermissions(grant.ExtraPermissions),
		EffectivePermissions: fromPermissions(grant.Effective()),
	}
}

func renderRBACServiceError(w http.ResponseWriter, err error) {
	validationErr := (*ValidationError)(nil)
	alreadyExistsErr := (*store.ErrResourceAlreadyExists)(nil)
	switch {
	case errors.Is(err, ErrRequiresControlPlane):
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrRequiresValidLicense):
		handlers.RenderError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrRoleNotFound):
		handlers.RenderError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrRoleInUse):
		handlers.RenderError(w, http.StatusConflict, err.Error())
	case errors.As(err, &alreadyExistsErr):
		handlers.RenderError(w, http.StatusConflict, alreadyExistsErr.Error())
	case errors.As(err, &validationErr):
		handlers.RenderError(w, http.StatusBadRequest, validationErr.Error())
	default:
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
	}
}

func (h *RBACHandler) ListRolesHandler(w http.ResponseWriter, r *http.Request) {
	roles, err := h.service.ListRoles(r.Context())
	if err != nil {
		renderRBACServiceError(w, err)
		return
	}
	response := make([]RoleResponse, len(roles))
	for i, role := range roles {
		response[i] = roleResponseFrom(role)
	}
	handlers.RenderJSON(w, http.StatusOK, response)
}

type rolePayload struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

func (h *RBACHandler) CreateRoleHandler(w http.ResponseWriter, r *http.Request) {
	var requestBody rolePayload
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	role, err := h.service.CreateRole(r.Context(), requestBody.Name, toPermissions(requestBody.Permissions))
	if err != nil {
		renderRBACServiceError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusCreated, roleResponseFrom(role))
}

func (h *RBACHandler) UpdateRoleHandler(w http.ResponseWriter, r *http.Request) {
	var requestBody rolePayload
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if err := h.service.UpdateRole(r.Context(), mux.Vars(r)["ROLE_ID"], requestBody.Name, toPermissions(requestBody.Permissions)); err != nil {
		renderRBACServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RBACHandler) DeleteRoleHandler(w http.ResponseWriter, r *http.Request) {
	if err := h.service.DeleteRole(r.Context(), mux.Vars(r)["ROLE_ID"]); err != nil {
		renderRBACServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveTargetUser 404s grant requests aimed at an account that does not
// exist, instead of answering an empty grant list that reads like a real,
// permissionless user.
func (h *RBACHandler) resolveTargetUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	userId := mux.Vars(r)["USER_ID"]
	if h.service.userLookup == nil {
		handlers.RenderError(w, http.StatusBadRequest, ErrRequiresControlPlane.Error())
		return "", false
	}
	if _, err := h.service.userLookup.GetUserByID(r.Context(), userId); err != nil {
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			handlers.RenderError(w, http.StatusNotFound, notFoundErr.Error())
		} else {
			handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
		}
		return "", false
	}
	return userId, true
}

func (h *RBACHandler) GetUserGrantsHandler(w http.ResponseWriter, r *http.Request) {
	userId, ok := h.resolveTargetUser(w, r)
	if !ok {
		return
	}
	grants, err := h.service.GetUserGrants(r.Context(), userId)
	if err != nil {
		renderRBACServiceError(w, err)
		return
	}
	response := make([]GrantResponse, len(grants))
	for i, grant := range grants {
		response[i] = grantResponseFrom(grant)
	}
	handlers.RenderJSON(w, http.StatusOK, response)
}

func (h *RBACHandler) SetUserGrantsHandler(w http.ResponseWriter, r *http.Request) {
	userId, ok := h.resolveTargetUser(w, r)
	if !ok {
		return
	}
	var requestBody []struct {
		AppId            string   `json:"appId"`
		RoleId           *string  `json:"roleId"`
		ExtraPermissions []string `json:"extraPermissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	grants := make([]GrantInput, len(requestBody))
	for i, grant := range requestBody {
		grants[i] = GrantInput{
			AppID:            grant.AppId,
			RoleID:           grant.RoleId,
			ExtraPermissions: toPermissions(grant.ExtraPermissions),
		}
	}
	if err := h.service.SetUserGrants(r.Context(), userId, grants); err != nil {
		renderRBACServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetGrantSummaryHandler answers the per-user grant counts as a plain
// {userId: count} map; users absent from the map hold no grants.
func (h *RBACHandler) GetGrantSummaryHandler(w http.ResponseWriter, r *http.Request) {
	counts, err := h.service.GrantCountsByUser(r.Context())
	if err != nil {
		renderRBACServiceError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, counts)
}

// MyPermissionsResponse tells the dashboard what to show the current account.
//
// RBACEnabled answers "are fine-grained roles enforced right now", which needs
// a control plane AND a valid enterprise license. False means the community
// rules apply and isAdmin decides everything. It is named for the mechanism
// and not just "enabled" because the accounts themselves carry an enabled
// flag, and the two are unrelated: a disabled account cannot obtain a session
// at all, which is settled long before this response is built.
//
// For an admin, or when roles are not enforced, Apps is null: there is nothing
// per-app to say, the answer is the same everywhere.
type MyPermissionsResponse struct {
	RBACEnabled bool                `json:"rbacEnabled"`
	IsAdmin     bool                `json:"isAdmin"`
	Apps        map[string][]string `json:"apps"`
}

// GetMyPermissionsHandler is display support only: the server re-checks every
// mutation through the middlewares regardless of what the UI shows.
func (h *RBACHandler) GetMyPermissionsHandler(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.service.resolveSubject(w, r)
	if !ok {
		return
	}
	response := MyPermissionsResponse{RBACEnabled: h.service.Enabled(), IsAdmin: subject.IsAdmin}
	if response.RBACEnabled && !subject.IsAdmin {
		byApp, err := h.service.EffectivePermissionsByApp(r.Context(), subject.UserID)
		if err != nil {
			renderRBACServiceError(w, err)
			return
		}
		response.Apps = make(map[string][]string, len(byApp))
		for appId, perms := range byApp {
			response.Apps[appId] = fromPermissions(perms)
		}
	}
	handlers.RenderJSON(w, http.StatusOK, response)
}
