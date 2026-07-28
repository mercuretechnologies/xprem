package handlers

import (
	"encoding/json"
	"errors"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/ratelimit"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/mux"
)

type UsersHandler struct {
	userService *services.UserService
	// dashboardAuthService mints the replacement session handed back when an
	// account changes its own password, which revokes every session it held.
	dashboardAuthService *services.DashboardAuthService
	limiter              *ratelimit.Limiter
}

func NewUsersHandler(userService *services.UserService, dashboardAuthService *services.DashboardAuthService, limiter *ratelimit.Limiter) *UsersHandler {
	return &UsersHandler{
		userService:          userService,
		dashboardAuthService: dashboardAuthService,
		limiter:              limiter,
	}
}

// UserResponse is the public shape of a user account. Id is empty in
// stateless mode, where the account is ADMIN_EMAIL and not a database row.
// LastConnectedAt is empty until the account's first successful sign-in.
type UserResponse struct {
	Id      string `json:"id"`
	Email   string `json:"email"`
	IsAdmin bool   `json:"isAdmin"`
	// Enabled is false for an account an admin revoked, or one awaiting
	// approval under SSO manual validation.
	Enabled         bool   `json:"enabled"`
	CreatedAt       string `json:"createdAt,omitempty"`
	LastConnectedAt string `json:"lastConnectedAt,omitempty"`
}

func userResponseFrom(user store.User) UserResponse {
	createdAt := ""
	if !user.CreatedAt.IsZero() {
		createdAt = user.CreatedAt.UTC().Format(time.RFC3339)
	}
	lastConnectedAt := ""
	if user.LastConnectedAt != nil {
		lastConnectedAt = user.LastConnectedAt.UTC().Format(time.RFC3339)
	}
	return UserResponse{
		Id:              user.Id,
		Email:           user.Email,
		IsAdmin:         user.IsAdmin,
		Enabled:         user.Enabled,
		CreatedAt:       createdAt,
		LastConnectedAt: lastConnectedAt,
	}
}

// renderUserServiceError maps the user service's business-rule errors onto
// explicit status codes; anything unrecognized stays an opaque 500.
func renderUserServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, services.ErrUsersRequireControlPlane),
		errors.Is(err, services.ErrCannotChangeOwnAdminFlag),
		errors.Is(err, services.ErrCannotDeleteOwnAccount),
		errors.Is(err, services.ErrCannotDisableOwnAccount),
		errors.Is(err, services.ErrInvalidCurrentPassword):
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrLastAdmin),
		errors.Is(err, services.ErrUserCreationDisabledBySSO):
		handlers.RenderError(w, http.StatusConflict, err.Error())
	default:
		if validationErr := (*services.ValidationError)(nil); errors.As(err, &validationErr) {
			handlers.RenderError(w, http.StatusBadRequest, validationErr.Error())
			return
		}
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			handlers.RenderError(w, http.StatusNotFound, notFoundErr.Error())
			return
		}
		if alreadyExistsErr := (*store.ErrResourceAlreadyExists)(nil); errors.As(err, &alreadyExistsErr) {
			handlers.RenderError(w, http.StatusConflict, alreadyExistsErr.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
	}
}

// GetMeHandler answers with the account behind the current session. In
// control-plane mode the service re-reads the row so the dashboard sees a
// revoked admin flag on its next load, not at token refresh.
func (h *UsersHandler) GetMeHandler(w http.ResponseWriter, r *http.Request) {
	principal := services.PrincipalFromContext(r.Context())
	if principal == nil {
		handlers.RenderError(w, http.StatusUnauthorized, "This route requires a dashboard session")
		return
	}
	user, err := h.userService.GetMe(r.Context(), principal.UserId, principal.Email)
	if err != nil {
		// Only a missing row means the session is a leftover of a deleted
		// account; an infrastructure failure must not read as a dead session.
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			handlers.RenderError(w, http.StatusUnauthorized, "Invalid token")
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
		return
	}
	handlers.RenderJSON(w, http.StatusOK, userResponseFrom(user))
}

func (h *UsersHandler) ChangeMyPasswordHandler(w http.ResponseWriter, r *http.Request) {
	principal := services.PrincipalFromContext(r.Context())
	if principal == nil {
		handlers.RenderError(w, http.StatusUnauthorized, "This route requires a dashboard session")
		return
	}
	var requestBody struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if requestBody.CurrentPassword == "" || requestBody.NewPassword == "" {
		handlers.RenderError(w, http.StatusBadRequest, "currentPassword and newPassword are required")
		return
	}
	// This route is behind authentication, so the attacker it guards against
	// already holds a session and is trying to turn it into the password
	// itself, which is what would survive revoking that session.
	if decision := h.limiter.CheckPasswordChange(principal.UserId); !decision.Allowed {
		handlers.RenderThrottled(w, decision.RetryAfter)
		return
	}
	err := h.userService.ChangePassword(r.Context(), principal.UserId, requestBody.CurrentPassword, requestBody.NewPassword)
	if err != nil {
		// Only a wrong current password counts. The other failures here are
		// validation of the NEW password and infrastructure, and throttling
		// someone for choosing a password that is too short would lock them
		// out of the fix.
		if errors.Is(err, services.ErrInvalidCurrentPassword) {
			h.limiter.RecordPasswordChangeFailure(principal.UserId)
		}
		renderUserServiceError(w, err)
		return
	}
	h.limiter.RecordPasswordChangeSuccess(principal.UserId)
	// Changing the password revoked every session the account held, including
	// the one that just made this call. Handing back a fresh pair keeps the
	// person who typed the password signed in, while every OTHER session stays
	// dead: the forgotten laptop, the attacker's stolen token. Doing it here
	// rather than in the service keeps the account rules and the session
	// plumbing apart.
	user, err := h.userService.GetMe(r.Context(), principal.UserId, principal.Email)
	if err == nil {
		var session *services.DashboardSession
		if session, err = h.dashboardAuthService.IssueSession(r.Context(), user); err == nil {
			handlers.RenderJSON(w, http.StatusOK, session)
			return
		}
	}
	// The password DID change, so answering an error would tell the client the
	// opposite of what happened. 204 is the same success with no renewed
	// session attached, which sends the client to the sign-in page.
	log.Printf("password changed for user %s but the session could not be renewed: %v", principal.UserId, err)
	w.WriteHeader(http.StatusNoContent)
}

func (h *UsersHandler) GetUsersHandler(w http.ResponseWriter, r *http.Request) {
	users, err := h.userService.GetUsers(r.Context())
	if err != nil {
		renderUserServiceError(w, err)
		return
	}
	response := make([]UserResponse, len(users))
	for i, user := range users {
		response[i] = userResponseFrom(user)
	}
	handlers.RenderJSON(w, http.StatusOK, response)
}

func (h *UsersHandler) CreateUserHandler(w http.ResponseWriter, r *http.Request) {
	var requestBody struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"isAdmin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if requestBody.Email == "" || requestBody.Password == "" {
		handlers.RenderError(w, http.StatusBadRequest, "email and password are required")
		return
	}
	user, err := h.userService.CreateUser(r.Context(), requestBody.Email, requestBody.Password, requestBody.IsAdmin)
	if err != nil {
		renderUserServiceError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusCreated, userResponseFrom(user))
}

// UpdateUserHandler patches the two admin-controlled flags of an account. Both
// fields are optional pointers so a request can carry either one without the
// absent one reading as "set to false"; sending both applies both.
func (h *UsersHandler) UpdateUserHandler(w http.ResponseWriter, r *http.Request) {
	principal := services.PrincipalFromContext(r.Context())
	if principal == nil {
		handlers.RenderError(w, http.StatusUnauthorized, "This route requires a dashboard session")
		return
	}
	targetUserId := mux.Vars(r)["USER_ID"]
	var requestBody struct {
		IsAdmin *bool `json:"isAdmin"`
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if requestBody.IsAdmin == nil && requestBody.Enabled == nil {
		handlers.RenderError(w, http.StatusBadRequest, "isAdmin or enabled is required")
		return
	}
	if requestBody.IsAdmin != nil {
		if err := h.userService.SetUserAdmin(r.Context(), principal.UserId, targetUserId, *requestBody.IsAdmin); err != nil {
			renderUserServiceError(w, err)
			return
		}
	}
	if requestBody.Enabled != nil {
		if err := h.userService.SetUserEnabled(r.Context(), principal.UserId, targetUserId, *requestBody.Enabled); err != nil {
			renderUserServiceError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *UsersHandler) DeleteUserHandler(w http.ResponseWriter, r *http.Request) {
	principal := services.PrincipalFromContext(r.Context())
	if principal == nil {
		handlers.RenderError(w, http.StatusUnauthorized, "This route requires a dashboard session")
		return
	}
	targetUserId := mux.Vars(r)["USER_ID"]
	err := h.userService.DeleteUser(r.Context(), principal.UserId, targetUserId)
	if err != nil {
		renderUserServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
