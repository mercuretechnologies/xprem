package infrastructure

import (
	"expo-open-ota/ee/rbac"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/services"
	"net/http"

	"github.com/gorilla/mux"
)

// Access is a route's answer to "who may call this", and every app-scoped
// route states one. It replaces two things that used to be implicit: whether a
// publishing token may reach the route (which used to follow from the ABSENCE
// of a permission gate, so it was never decided, only inherited) and what a
// member gets when roles are not enforced (which used to be hardcoded in the
// service, the same for every route).
//
// The type is a value with no exported fields on purpose. It can only be built
// through the four constructors below, and each of them is internally
// consistent: there is no way to write "no permission required" and "admins
// only when roles are off" at the same time, because that pair contradicts
// itself and would read as a guard while behaving as none.
type Access struct {
	// token: may a validated CLI publishing credential reach the handler. A
	// token carries no account, so RBAC cannot apply to it: on the routes that
	// allow one, the app scope its credential was validated against IS the
	// authorisation.
	token bool
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

// AnyViewerOrToken is AnyViewer, plus a publishing credential. Reserved for
// what the eoas CLI genuinely calls: everything else stays out of a token's
// reach, because a token is usually a shared CI secret and the app-scoped
// reads carry device-level data.
func AnyViewerOrToken() Access {
	return Access{declared: true, perm: rbac.NoPermission, token: true}
}

// NeedsPermission gates the route behind perm once roles are enforced, and
// behind fallback when they are not.
func NeedsPermission(perm rbac.Permission, fallback rbac.Fallback) Access {
	return Access{declared: true, perm: perm, fallback: fallback}
}

// appGroup registers the app-scoped routes and is the only way to add one:
// route() takes the Access declaration as an argument it cannot default, so a
// new route cannot be added without answering the question.
type appGroup struct {
	router      *mux.Router
	rbacService *rbac.RBACService
}

func (g appGroup) route(method, path string, handler http.HandlerFunc, access Access) {
	if !access.declared {
		// At boot, so it lands in the first test that builds a router rather
		// than in production on the first request to the route.
		panic("router: " + method + " " + path + " was registered without an Access declaration")
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
			if services.CliAuthFromContext(r.Context()) != nil {
				if !access.token {
					handlers.RenderError(w, http.StatusForbidden, "This route requires a dashboard session")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			gated.ServeHTTP(w, r)
		})
	}
}
