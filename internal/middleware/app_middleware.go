package middleware

import (
	"net/http"
	"xprem/config"
	"xprem/internal/handlers"
	"xprem/internal/services"

	"github.com/gorilla/mux"
)

// AppResolverMiddleware extracts APP_ID from the path vars and validates it
// (well-formed AND registered in the apps config). Downstream handlers read
// the id back from mux.Vars themselves — the middleware's job is just to
// short-circuit bad/unknown ids before they reach the handler.
//
// The registry check matches the manifest/assets handlers' edge behavior:
// unknown app ids return 404 here, instead of falling through to handlers
// that try to validate the request against api.expo.dev with no token and
// surface that as a misleading 401.
func AppResolverMiddleware(appRepository services.AppRepository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			appId := mux.Vars(r)["APP_ID"]
			if !isValidAppID(appId) {
				handlers.RenderError(w, http.StatusBadRequest, "invalid app id")
				return
			}
			if _, err := appRepository.GetAppByID(r.Context(), appId); err != nil {
				handlers.RenderError(w, http.StatusNotFound, "app not found")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isValidAppID short-circuits obviously-malformed ids before hitting the
// registry. Delegates to config.ValidateAppId so rules cannot drift from
// boot-time validation. The field path passed to the validator is used
// only for its error string, which the middleware discards.
func isValidAppID(id string) bool {
	return config.ValidateAppId(id, "appId") == nil
}
