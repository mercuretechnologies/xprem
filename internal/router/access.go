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

// branchVar is the ONLY path variable a publishing token may be scoped on. A
// route that lets a token in has to carry it, and route() refuses to register
// one that does not: an access rule is written about branches, so a route with
// no branch is a route no rule can judge.
const (
	branchVarName = "BRANCH"
	branchVar     = "{" + branchVarName + "}"
)

// Access is a route's answer to "who may call this", and every app-scoped
// route states one. It replaces two things that used to be implicit: whether a
// publishing token may reach the route (which used to follow from the ABSENCE
// of a permission gate, so it was never decided, only inherited) and what a
// member gets when roles are not enforced (which used to be hardcoded in the
// service, the same for every route).
//
// The type is a value with no exported fields on purpose. It can only be built
// through the constructors below, and each of them is internally consistent:
// there is no way to write "no permission required" and "admins only when
// roles are off" at the same time, because that pair contradicts itself and
// would read as a guard while behaving as none.
type Access struct {
	// token: may a validated CLI publishing credential reach the handler, and
	// what does it do here. A token carries no account, so RBAC cannot judge
	// it; its API key's access rules can, and tokenAction is what they are
	// judged against.
	token       bool
	tokenAction apikeyrestrictions.Action
	// perm: what a member needs once roles are enforced. NoPermission means
	// the route asks nothing beyond being able to see the app.
	perm rbac.Permission
	// fallback: what a non-admin member gets when roles are NOT enforced.
	// Meaningless when perm is NoPermission, which is why no constructor lets
	// the two be set together.
	fallback rbac.Fallback
	// declared separates a real declaration from the zero value. Go cannot
	// stop `Access{}` from being written, so the registration helper refuses
	// it at boot instead: every test that builds a router runs that check.
	declared bool
}

// AnyViewer: any account that may see the app. RequireAppVisible on the group
// already answered that question, so there is nothing left to ask.
func AnyViewer() Access {
	return Access{declared: true, perm: rbac.NoPermission}
}

// AnyViewerOrToken is AnyViewer, plus a publishing credential doing action on
// the route's {BRANCH}. Reserved for what the eoas CLI genuinely calls:
// everything else stays out of a token's reach, because a token is usually a
// shared CI secret and the app-scoped reads carry device-level data.
func AnyViewerOrToken(action apikeyrestrictions.Action) Access {
	if !apikeyrestrictions.IsValidAction(string(action)) {
		panic("router: AnyViewerOrToken called with an unknown action " + string(action))
	}
	return Access{declared: true, perm: rbac.NoPermission, token: true, tokenAction: action}
}

// NeedsPermission gates the route behind perm once roles are enforced, and
// behind fallback when they are not.
//
// Both arguments are checked rather than trusted. NoPermission here would make
// guard skip the permission middleware entirely, so the registration line
// would read as a gate while the route behaved exactly like AnyViewer; a zero
// Fallback would refuse every member once a license lapses, which fails closed
// but for a reason nobody wrote down. Neither is a combination anyone means,
// and both are caught at boot rather than in production.
func NeedsPermission(perm rbac.Permission, fallback rbac.Fallback) Access {
	if perm == rbac.NoPermission {
		panic("router: NeedsPermission called with NoPermission, which gates nothing; use AnyViewer() if that is the intent")
	}
	if fallback != rbac.FallbackAdminOnly && fallback != rbac.FallbackAnyMember {
		panic("router: NeedsPermission called without a Fallback; say what a member gets when roles are not enforced")
	}
	return Access{declared: true, perm: perm, fallback: fallback}
}

// cliAccessPolicy is the access decision the guard asks. It is an interface
// rather than the concrete enterprise service so this package's tests can
// observe what a route hands it: the branch and the action a route declares
// are the whole subject of the guard, and asserting them needs a seam.
type cliAccessPolicy interface {
	Authorize(ctx context.Context, req apikeyrestrictions.CliRequest) error
}

// appGroup registers the app-scoped routes and is the only way to add one:
// route() takes the Access declaration as an argument it cannot default, so a
// new route cannot be added without answering the question.
type appGroup struct {
	router       *mux.Router
	rbacService  *rbac.RBACService
	apiKeyAccess cliAccessPolicy
}

func (g appGroup) route(method, path string, handler http.HandlerFunc, access Access) {
	if !access.declared {
		// At boot, so it lands in the first test that builds a router rather
		// than in production on the first request to the route.
		panic("router: " + method + " " + path + " was registered without an Access declaration")
	}
	// The hole this closes: a token-reachable route with no branch in its path
	// leaves the access rules with nothing to match, and the honest reading of
	// that is "no rule applies", which is one word away from "everything is
	// allowed". Refusing at boot means the question cannot be forgotten, and
	// it also pins the SPELLING: a route declared {BRANCH_ID} does not pass.
	if access.token && !strings.Contains(path, branchVar) {
		panic("router: " + method + " " + path + " lets a publishing token in but names no " + branchVar +
			"; a token is scoped to branches, so a branchless route cannot be judged")
	}
	g.router.Handle(path, g.guard(access)(handler)).Methods(method)
}

// guard turns one Access into the middleware that enforces it. The permission
// middleware is built once here rather than per request.
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
			// A publishing credential is handled first and on its own: it has
			// no account, so the permission path below cannot judge it. Only
			// the routes that named a token get one.
			if credential := services.CliAuthFromContext(r.Context()); credential != nil {
				if !access.token {
					handlers.RenderError(w, http.StatusForbidden, "This route requires a dashboard session")
					return
				}
				// The credential is already on the context, put there by the
				// authentication middleware, so nothing has to be stamped back.
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

// authorizeCliRequest runs the enterprise access decision. It answers false
// when the request was refused, having already written the response, and it
// touches nothing else: putting the credential on the context belongs to
// whichever group needs to, which is only the publish group.
//
// This is the only place the decision is taken, and nothing downstream repeats
// it: a handler trusts it exactly as it trusts the RBAC permission middleware
// and the dashboard session, because the registration groups in this package
// are the only way a route reaches a handler at all.
func authorizeCliRequest(
	policy cliAccessPolicy,
	w http.ResponseWriter,
	r *http.Request,
	credential services.CliCredential,
	action apikeyrestrictions.Action,
	branchName string,
) bool {
	// No branch, no decision. A token route always names one, and route()
	// refuses at boot to register one that does not, so this is unreachable
	// through the declaration path. It is checked anyway, because the
	// alternative is a request judged on an empty branch whose only safeguard
	// then lives in another package (AllowsBranch refusing a scoped key), and
	// borrowed safety is how a bypass arrives through a refactor that looked
	// unrelated. A resolver that cannot name a branch has not authorized
	// anything, whatever the rules would have said.
	//
	// It also covers the one resolver that legitimately answers nothing,
	// uploadTokenBranch, when the token is absent, expired, foreign-signed or
	// predates the branch claim. Refusing here rather than downstream costs a
	// broken upload token a 403 instead of the handler's 400, which is the same
	// failure with a clearer cause.
	if branchName == "" {
		handlers.RenderError(w, http.StatusForbidden, "This route requires a branch")
		return false
	}
	// A key id of 0 is stateless mode: no API key exists, so there is nothing
	// to carry access rules and nothing to enforce.
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
