package infrastructure

import (
	"context"
	"net/http"
	"strings"
	"xprem/ee/apikeyrestrictions"
	"xprem/ee/rbac"
	"xprem/internal/handlers"
	"xprem/internal/services"

	"github.com/gorilla/mux"
)

// AppAccess is a route's declaration of who may call it: whether a publishing
// token may reach it, and what a member needs when RBAC is enforced or not.
type AppAccess struct {
	token       bool
	tokenAction apikeyrestrictions.Action
	perm        rbac.Permission
	fallback    rbac.Fallback
	// declared distinguishes a real declaration from the zero value.
	declared bool
}

// AnyViewer allows any account that may see the app.
func AnyViewer() AppAccess {
	return AppAccess{declared: true, perm: rbac.NoPermission}
}

// AnyViewerOrToken is AnyViewer, plus a publishing credential authorized for
// action on the route's {BRANCH}.
func AnyViewerOrToken(action apikeyrestrictions.Action) AppAccess {
	if !apikeyrestrictions.IsValidAction(string(action)) {
		panic("router: AnyViewerOrToken called with an unknown action " + string(action))
	}
	return AppAccess{declared: true, perm: rbac.NoPermission, token: true, tokenAction: action}
}

// NeedsPermission gates the route behind perm once roles are enforced, and
// behind fallback when they are not.
func NeedsPermission(perm rbac.Permission, fallback rbac.Fallback) AppAccess {
	if perm == rbac.NoPermission {
		panic("router: NeedsPermission called with NoPermission, which gates nothing; use AnyViewer() if that is the intent")
	}
	if fallback != rbac.FallbackAdminOnly && fallback != rbac.FallbackAnyMember {
		panic("router: NeedsPermission called without a Fallback; say what a member gets when roles are not enforced")
	}
	return AppAccess{declared: true, perm: perm, fallback: fallback}
}

// cliAccessPolicy is the access decision the guard asks for a CLI request.
type cliAccessPolicy interface {
	Authorize(ctx context.Context, req apikeyrestrictions.CliRequest) error
}

// appGroup registers the app-scoped routes.
type appGroup struct {
	router       *mux.Router
	rbacService  *rbac.RBACService
	apiKeyAccess cliAccessPolicy
}

func (g appGroup) route(method, path string, handler http.HandlerFunc, access AppAccess) {
	if !access.declared {
		panic("router: " + method + " " + path + " was registered without an AppAccess declaration")
	}
	if access.token && !strings.Contains(path, branchVar) {
		panic("router: " + method + " " + path + " lets a publishing token in but names no " + branchVar +
			"; a token is scoped to branches, so a branchless route cannot be judged")
	}
	g.router.Handle(path, g.guard(access)(handler)).Methods(method)
}

// guard turns one AppAccess into the middleware that enforces it.
func (g appGroup) guard(access AppAccess) mux.MiddlewareFunc {
	var permission mux.MiddlewareFunc
	if access.perm != rbac.NoPermission {
		permission = rbac.RequirePermission(g.rbacService, access.perm, access.fallback)
	}

	return func(next http.Handler) http.Handler {
		gated := next
		if permission != nil {
			gated = permission(next)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if credential := services.CliAuthFromContext(r.Context()); credential != nil {
				if !access.token {
					handlers.RenderError(w, http.StatusForbidden, "This route requires a dashboard session")
					return
				}
				if !authorizeCliRequest(g.apiKeyAccess, w, r, *credential, access.tokenAction, mux.Vars(r)[branchVarName]) {
					return
				}
				gated.ServeHTTP(w, r)
				return
			}
			gated.ServeHTTP(w, r)
		})
	}
}
