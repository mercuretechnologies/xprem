package infrastructure

import (
	"expo-open-ota/ee/apikeyrestrictions"
	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/middleware"
	"expo-open-ota/internal/services"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
)

// The publish flow, called by the eoas CLI from a developer machine or a CI
// job. These five routes are the whole write path of an update: ask for the
// upload URLs, push the files, seal the update, then the two operations that
// move a branch's pointer afterwards.
//
// AUTHENTICATION AND AUTHORISATION happen here, in front of the handlers, and
// each route says which action it performs on its {BRANCH}. That declaration
// is the point: an API key's access rules are written about branches and
// actions, so the layer that judges them has to hold both, and only the
// routing table does. It used to live in the handlers, one hand-written call
// per handler, which is a rule you can forget on the sixth one.
//
// AppResolverMiddleware still runs first and turns an unknown APP_ID into a
// 404 before any of that.
func registerPublishRoutes(r *mux.Router, container *AppContainer) {
	appSubrouter := r.PathPrefix("/{APP_ID}").Subrouter()
	appSubrouter.Use(middleware.AppResolverMiddleware(container.AppRepo))

	publish := publishGroup{
		router:       appSubrouter,
		cliAuth:      container.CliAuthService,
		apiKeyAccess: container.ApiKeyAccessService,
	}

	for _, declaration := range publishRouteTable {
		publish.route(declaration.method, declaration.path, declaration.handler(container), declaration.action)
	}

	// The odd one out: the local-bucket file upload names no branch in its
	// path, it carries a short-lived signed token minted by the upload-url
	// request above. The branch is a claim of that token, so the guard reads
	// it from there and judges the request like any other publish. Answering
	// "no branch, no rules to apply, let it through" instead would have left a
	// scoped key able to write files onto any branch it can name.
	publish.uploadTokenRoute(http.MethodPut, "/uploadLocalFile", container.UploadHandler.RequestUploadLocalFileHandler)
}

// publishRouteTable is the declaration of every branch-scoped publish route:
// what it is, and what it does to the branch it names. It is a table rather
// than four calls so a test can read the same declarations the router
// registers — an action written wrong here is a silent privilege change (a key
// granted only read on production could roll production back), and that is
// worth pinning against the source of truth rather than against a copy.
var publishRouteTable = []struct {
	method  string
	path    string
	action  apikeyrestrictions.Action
	handler func(*AppContainer) http.HandlerFunc
}{
	{http.MethodPost, "/requestUploadUrl/{BRANCH}", apikeyrestrictions.ActionPublish,
		func(c *AppContainer) http.HandlerFunc { return c.UploadHandler.RequestUploadUrlHandler }},
	{http.MethodPost, "/markUpdateAsUploaded/{BRANCH}", apikeyrestrictions.ActionPublish,
		func(c *AppContainer) http.HandlerFunc { return c.UploadHandler.MarkUpdateAsUploadedHandler }},
	{http.MethodPost, "/rollback/{BRANCH}", apikeyrestrictions.ActionRollback,
		func(c *AppContainer) http.HandlerFunc { return c.RollbackHandler.HandleRollback }},
	{http.MethodPost, "/republish/{BRANCH}", apikeyrestrictions.ActionRollback,
		func(c *AppContainer) http.HandlerFunc { return c.RepublishHandler.HandleRepublish }},
}

// publishGroup registers the CLI write routes. Like appGroup, its route()
// cannot default the declaration: adding a publish route means saying what it
// does to the branch it names.
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
// from its signed upload token rather than its path.
func (g publishGroup) uploadTokenRoute(method, path string, handler http.HandlerFunc) {
	g.router.Handle(path, g.guard(apikeyrestrictions.ActionPublish, uploadTokenBranch)(handler)).Methods(method)
}

// branchResolver answers which branch a request acts on.
type branchResolver func(r *http.Request) string

func routeBranch(r *http.Request) string {
	return mux.Vars(r)[branchVarName]
}

// uploadTokenBranch reads the branch out of the upload token, through the same
// validation the handler runs, so the two cannot disagree about what a valid
// upload token is. Anything unreadable, expired, foreign-signed or claimless
// yields "", which the access rules refuse for a scoped key; the handler then
// refuses the same token on its own.
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
			authorized, ok := authorizeCliRequest(g.apiKeyAccess, w, r, credential, action, resolveBranch(r))
			if !ok {
				return
			}
			next.ServeHTTP(w, authorized)
		})
	}
}
