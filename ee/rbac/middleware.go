// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"errors"
	"expo-open-ota/internal/auditlog"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"
	"net/http"

	"github.com/gorilla/mux"
)

// UserLookup is the one read the middlewares need from the users store.
// services.UserRepository satisfies it; keeping it narrow lets tests fake a
// single method instead of the whole repository. Nil in stateless mode, where
// the session claim is authoritative (the single ADMIN_EMAIL account).
type UserLookup interface {
	GetUserByID(ctx context.Context, id string) (store.User, error)
}

// resolveSubject authenticates the request as a dashboard account and
// resolves its admin flag from a fresh users-table read, exactly like the
// community admin gate: a session token lives 2 hours, and a revoked admin
// (or deleted user) must lose access immediately, not at the next refresh.
// On failure it writes the response and returns ok=false.
func (s *RBACService) resolveSubject(w http.ResponseWriter, r *http.Request) (Subject, bool) {
	subject, err := s.subjectFromContext(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, ErrNoDashboardSession):
			handlers.RenderError(w, http.StatusForbidden, "This action requires a dashboard session")
		case errors.Is(err, errAccountGone):
			handlers.RenderError(w, http.StatusUnauthorized, "Invalid token")
		default:
			handlers.RenderError(w, http.StatusInternalServerError, "Could not verify the account")
		}
		return Subject{}, false
	}
	return subject, true
}

var (
	// ErrNoDashboardSession is the request that carries no dashboard principal
	// (a CLI credential, or nothing at all).
	ErrNoDashboardSession = errors.New("this action requires a dashboard session")
	// errAccountGone is the account whose row is missing: deleted since the
	// token was minted. Kept apart from an infrastructure failure, which must
	// not read as a dead session.
	errAccountGone = errors.New("account no longer exists")
)

// subjectFromContext is resolveSubject without the rendering, for the callers
// that are not middlewares. It is the whole "who is asking" rule, so the ones
// that need a Subject outside the middleware chain share it rather than
// reimplementing the fresh admin read.
func (s *RBACService) subjectFromContext(ctx context.Context) (Subject, error) {
	principal := services.PrincipalFromContext(ctx)
	if principal == nil {
		return Subject{}, ErrNoDashboardSession
	}
	if s.userLookup == nil {
		// Stateless mode: the single ADMIN_EMAIL account is always an admin
		return Subject{UserID: principal.UserId, IsAdmin: principal.IsAdmin}, nil
	}
	user, err := s.userLookup.GetUserByID(ctx, principal.UserId)
	if err != nil {
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			return Subject{}, errAccountGone
		}
		return Subject{}, err
	}
	return Subject{UserID: principal.UserId, IsAdmin: user.IsAdmin}, nil
}

// RequirePermission guards one app-scoped dashboard action: admins pass,
// members need the permission on the route's APP_ID. When roles are not
// enforced (no control plane, no valid license) there are no grants to read,
// and the route's own fallback decides instead: see Fallback.
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

// authorizeRequest is the decision RequirePermission wraps, split out so the
// middleware stays three lines. Deliberately unexported: a handler that
// authorizes itself is a permission nobody can find by reading the routing
// table, and the one endpoint that seemed to need it turned out to be two
// endpoints wearing one URL.
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

// recordDenied reports a refusal to the audit trail: permission.denied is the
// single event for authorization refusals (see the audit catalog). Only real
// denials are events — the community fallback's admin-only refusals happen
// precisely when no license is active, so the recorder would drop them anyway.
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
	// The method and path disambiguate what was attempted when one permission
	// covers several endpoints (apikeys:manage gates create, revoke and
	// restrictions).
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
		// admin-only gate so an expired license reads identically to today.
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

// RequireAppVisible guards the app-scoped dashboard reads: while roles are
// enforced, members only see the apps they hold a grant on — anything else
// 404s like an app that does not exist. Admins and the community fallback see
// everything. CLI credentials pass through on the explicit marker the auth
// middleware stamped after validating their app scope — asserted, not
// inferred from a missing principal, so a wiring mistake fails closed.
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
