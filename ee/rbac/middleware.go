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

// UserLookup resolves a user id to its row; nil in stateless mode.
type UserLookup interface {
	GetUserByID(ctx context.Context, id string) (store.User, error)
}

// resolveSubject reads the dashboard account the auth middleware resolved. On
// failure it writes the response and returns ok=false.
func resolveSubject(w http.ResponseWriter, r *http.Request) (Subject, bool) {
	principal := services.PrincipalFromContext(r.Context())
	if principal == nil {
		handlers.RenderError(w, http.StatusForbidden, "This action requires a dashboard session")
		return Subject{}, false
	}
	return subjectFor(principal), true
}

// grantContextKey carries the grant RequireAppVisible loaded so that
// RequirePermission judges the same row instead of reading it again.
type grantContextKey struct{}

func withGrant(ctx context.Context, grant *AppGrant) context.Context {
	return context.WithValue(ctx, grantContextKey{}, grant)
}

// grantFromContext returns the grant loaded earlier in the request; loaded is
// false when no middleware stored one (admins and the community fallback).
func grantFromContext(ctx context.Context) (grant *AppGrant, loaded bool) {
	grant, loaded = ctx.Value(grantContextKey{}).(*AppGrant)
	return grant, loaded
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
	subject, ok := resolveSubject(w, r)
	if !ok {
		return false
	}
	appId := mux.Vars(r)["APP_ID"]
	if appId == "" {
		handlers.RenderError(w, http.StatusBadRequest, "invalid app id")
		return false
	}
	var err error
	if grant, loaded := grantFromContext(r.Context()); loaded {
		err = grantAllows(grant, perm)
	} else {
		err = service.Authorize(r.Context(), subject, appId, perm, fallback)
	}
	if err != nil {
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
			subject, ok := resolveSubject(w, r)
			if !ok {
				return
			}
			appId := mux.Vars(r)["APP_ID"]
			visible, grant, err := service.VisibleGrant(r.Context(), subject, appId)
			if err != nil {
				handlers.RenderError(w, http.StatusInternalServerError, "Could not verify permissions")
				return
			}
			if !visible {
				service.recordDenied(r, subject, appId, ErrNoAppAccess, map[string]any{})
				handlers.RenderError(w, http.StatusNotFound, "app not found")
				return
			}
			if grant != nil {
				r = r.WithContext(withGrant(r.Context(), grant))
			}
			next.ServeHTTP(w, r)
		})
	}
}
