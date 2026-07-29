package infrastructure

import (
	"expo-open-ota/ee/apikeyrestrictions"
	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/services"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
)

// publishGroup registers the CLI write routes.
type publishGroup struct {
	router       *mux.Router
	cliAuth      *services.CliAuthService
	apiKeyAccess cliAccessPolicy
}

func (g publishGroup) route(method, path string, handler http.HandlerFunc, action apikeyrestrictions.Action) {
	if !apikeyrestrictions.IsValidAction(string(action)) {
		panic("router: " + method + " " + path + " was registered with an unknown action " + string(action))
	}
	if !strings.Contains(path, branchVar) {
		panic("router: " + method + " " + path + " is a publish route but names no " + branchVar)
	}
	g.router.Handle(path, g.guard(action, routeBranch)(handler)).Methods(method)
}

// uploadTokenRoute is route() for the one publish route whose branch comes
// from its signed upload token rather than its path. It refuses a path that
// carries a branch variable, since that would be judged through route() instead.
func (g publishGroup) uploadTokenRoute(method, path string, handler http.HandlerFunc) {
	if strings.Contains(path, branchVar) {
		panic("router: " + method + " " + path + " names a " + branchVar +
			", so it must be registered with route(), which judges that branch rather than a token claim")
	}
	g.router.Handle(path, g.guard(apikeyrestrictions.ActionPublish, uploadTokenBranch)(handler)).Methods(method)
}

// branchResolver answers which branch a request acts on.
type branchResolver func(r *http.Request) string

func routeBranch(r *http.Request) string {
	return mux.Vars(r)[branchVarName]
}

// uploadTokenBranch reads the branch out of the upload token, through the
// same validation the handler runs. Anything unreadable, expired,
// foreign-signed or claimless yields "".
func uploadTokenBranch(r *http.Request) string {
	branchName, err := bucket.ResolveUploadTokenBranch(r.URL.Query().Get("token"))
	if err != nil {
		return ""
	}
	return branchName
}

func (g publishGroup) guard(action apikeyrestrictions.Action, resolveBranch branchResolver) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			credential, err := g.cliAuth.AuthenticateCliCredential(r.Context(), mux.Vars(r)["APP_ID"], helpers.GetAuth(r))
			if err != nil {
				handlers.RenderCliAuthError(w, err)
				return
			}
			if !authorizeCliRequest(g.apiKeyAccess, w, r, credential, action, resolveBranch(r)) {
				return
			}
			next.ServeHTTP(w, r.WithContext(services.WithCliAuth(r.Context(), credential)))
		})
	}
}
