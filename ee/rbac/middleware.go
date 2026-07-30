// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"errors"
	"net/http"
	"xprem/internal/auditlog"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/store"

	"github.com/gorilla/mux"
)

// UserLookup is the one read the middlewares need from the users store. It is
// nil in stateless mode, where the session claim (the single ADMIN_EMAIL
// account) is authoritative.
type UserLookup interface {
	GetUserByID(ctx context.Context, id string) (store.User, error)
}

// resolveSubject authenticates the request as a dashboard account and
// resolves its admin flag from a fresh users-table read. On failure it writes
// the response and returns ok=false.
func (s *RBACService) resolveSubject(w http.ResponseWriter, r *http.Request) (Subject, bool) {
	principal := services.PrincipalFromContext(r.Context())
	if principal == nil {
		handlers.RenderError(w, http.StatusForbidden, "This action requires a dashboard session")
		return Subject{}, false
	}
	if s.userLookup == nil {
		// Stateless mode: the single ADMIN_EMAIL account is always an admin
		return Subject{UserID: principal.UserId, IsAdmin: principal.IsAdmin}, true
	}
	user, err := s.userLookup.GetUserByID(r.Context(), principal.UserId)
	if err != nil {
		// Only a missing row means the account is gone; an infrastructure
		// failure must not read as a dead session.
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			handlers.RenderError(w, http.StatusUnauthorized, "Invalid token")
		} else {
			handlers.RenderError(w, http.StatusInternalServerError, "Could not verify the account")
		}
		return Subject{}, false
	}
	return Subject{UserID: principal.UserId, IsAdmin: user.IsAdmin}, true
}

// RequirePermission guards one app-scoped dashboard action: admins pass,
// members need the permission on the route's APP_ID. When roles are not
// enforced, the route's own fallback decides instead; see Fallback.
func RequirePermission(service *RBACService, perm Permission, fallback Fallback) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !authorizeRequest(service, w, r, perm, fallback) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// authorizeRequest is the decision RequirePermission wraps.
func authorizeRequest(
	service *RBACService,
	w http.ResponseWriter,
	r *http.Request,
	perm Permission,
	fallback Fallback,
) bool {
	subject, ok := service.resolveSubject(w, r)
	if !ok {
		return false
	}
	appId := mux.Vars(r)["APP_ID"]
	if appId == "" {
		handlers.RenderError(w, http.StatusBadRequest, "invalid app id")
		return false
	}
	if err := service.Authorize(r.Context(), subject, appId, perm, fallback); err != nil {
		service.recordDenied(r, subject, appId, err, map[string]any{"permission": string(perm)})
		renderAuthorizeError(w, err)
		return false
	}
	return true
}

// recordDenied reports a refusal to the audit trail. Only real denials are
// recorded; the community fallback's admin-only refusals are not.
func (s *RBACService) recordDenied(r *http.Request, subject Subject, appID string, cause error, metadata map[string]any) {
	if s.onAuditEvent == nil {
		return
	}
	deniedErr := (*ErrPermissionDenied)(nil)
	switch {
	case errors.As(cause, &deniedErr):
	case errors.Is(cause, ErrNoAppAccess):
		metadata["reason"] = "no_app_grant"
	default:
		return
	}
	// The method and path disambiguate when one permission covers several
	// endpoints.
	metadata["method"] = r.Method
	metadata["path"] = r.URL.Path
	actorDisplay := subject.UserID
	if principal := services.PrincipalFromContext(r.Context()); principal != nil && principal.Email != "" {
		actorDisplay = principal.Email
	}
	s.onAuditEvent(r.Context(), auditlog.Event{
		ActorType:    auditlog.ActorUser,
		ActorID:      subject.UserID,
		ActorDisplay: actorDisplay,
		Action:       auditlog.ActionPermissionDenied,
		TargetType:   "app",
		TargetID:     appID,
		AppID:        appID,
		Outcome:      auditlog.OutcomeDenied,
		Metadata:     metadata,
	})
}

func renderAuthorizeError(w http.ResponseWriter, err error) {
	deniedErr := (*ErrPermissionDenied)(nil)
	switch {
	case errors.Is(err, ErrRequiresControlPlane), errors.Is(err, ErrRequiresValidLicense):
		// Community fallback: members are read-only, same refusal as the
		// admin-only gate.
		handlers.RenderError(w, http.StatusForbidden, "This action requires an admin account")
	case errors.Is(err, ErrNoAppAccess):
		// Same body as the app resolver's 404: an app the member has no
		// grant on does not exist for them.
		handlers.RenderError(w, http.StatusNotFound, "app not found")
	case errors.As(err, &deniedErr):
		handlers.RenderError(w, http.StatusForbidden, deniedErr.Error())
	default:
		handlers.RenderError(w, http.StatusInternalServerError, "Could not verify permissions")
	}
}

// RequireAppVisible guards the app-scoped dashboard reads: members only see
// apps they hold a grant on; everything else 404s. Admins, the community
// fallback, and CLI credentials (marked after their app-scope check) pass
// through.
func RequireAppVisible(service *RBACService) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if services.PrincipalFromContext(r.Context()) == nil {
				if services.CliAuthAppFromContext(r.Context()) != "" {
					next.ServeHTTP(w, r)
					return
				}
				handlers.RenderError(w, http.StatusForbidden, "This action requires a dashboard session")
				return
			}
			subject, ok := service.resolveSubject(w, r)
			if !ok {
				return
			}
			visible, err := service.CanSeeApp(r.Context(), subject, mux.Vars(r)["APP_ID"])
			if err != nil {
				handlers.RenderError(w, http.StatusInternalServerError, "Could not verify permissions")
				return
			}
			if !visible {
				service.recordDenied(r, subject, mux.Vars(r)["APP_ID"], ErrNoAppAccess, map[string]any{})
				handlers.RenderError(w, http.StatusNotFound, "app not found")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
