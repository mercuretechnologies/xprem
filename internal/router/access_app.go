package infrastructure

import (
	"context"
	"expo-open-ota/ee/apikeyrestrictions"
	"expo-open-ota/ee/rbac"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/services"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
)

// Access is a route's declaration of who may call it: whether a publishing
// token may reach it, and what a member needs when RBAC is enforced or not.
type Access struct {
	token       bool
	tokenAction apikeyrestrictions.Action
	perm        rbac.Permission
	fallback    rbac.Fallback
	// declared distinguishes a real declaration from the zero value.
	declared bool
}

// AnyViewer allows any account that may see the app.
func AnyViewer() Access {
	return Access{declared: true, perm: rbac.NoPermission}
}

// AnyViewerOrToken is AnyViewer, plus a publishing credential authorized for
// action on the route's {BRANCH}.
func AnyViewerOrToken(action apikeyrestrictions.Action) Access {
	if !apikeyrestrictions.IsValidAction(string(action)) {
		panic("router: AnyViewerOrToken called with an unknown action " + string(action))
	}
	return Access{declared: true, perm: rbac.NoPermission, token: true, tokenAction: action}
}

// NeedsPermission gates the route behind perm once roles are enforced, and
// behind fallback when they are not.
func NeedsPermission(perm rbac.Permission, fallback rbac.Fallback) Access {
	if perm == rbac.NoPermission {
		panic("router: NeedsPermission called with NoPermission, which gates nothing; use AnyViewer() if that is the intent")
	}
	if fallback != rbac.FallbackAdminOnly && fallback != rbac.FallbackAnyMember {
		panic("router: NeedsPermission called without a Fallback; say what a member gets when roles are not enforced")
	}
	return Access{declared: true, perm: perm, fallback: fallback}
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

func (g appGroup) route(method, path string, handler http.HandlerFunc, access Access) {
	if !access.declared {
		panic("router: " + method + " " + path + " was registered without an Access declaration")
	}
	if access.token && !strings.Contains(path, branchVar) {
		panic("router: " + method + " " + path + " lets a publishing token in but names no " + branchVar +
			"; a token is scoped to branches, so a branchless route cannot be judged")
	}
	g.router.Handle(path, g.guard(access)(handler)).Methods(method)
}

// guard turns one Access into the middleware that enforces it.
func (g appGroup) guard(access Access) mux.MiddlewareFunc {
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

// authorizeCliRequest runs the enterprise access decision for a CLI request,
// writing the response and returning false when the request is refused.
func authorizeCliRequest(
	policy cliAccessPolicy,
	w http.ResponseWriter,
	r *http.Request,
	credential services.CliCredential,
	action apikeyrestrictions.Action,
	branchName string,
) bool {
	if branchName == "" {
		handlers.RenderError(w, http.StatusForbidden, "This route requires a branch")
		return false
	}
	// KeyID 0 is stateless mode: no API key exists to carry access rules.
	if credential.KeyID != 0 {
		err := policy.Authorize(r.Context(), apikeyrestrictions.CliRequest{
			AppID:    credential.AppID,
			APIKeyID: credential.KeyID,
			Branch:   branchName,
			Action:   action,
			ClientIP: helpers.ClientIP(r),
		})
		if err != nil {
			handlers.RenderCliAuthError(w, err)
			return false
		}
	}
	return true
}
