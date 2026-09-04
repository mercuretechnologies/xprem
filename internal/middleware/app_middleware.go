package middleware

import (
	"net/http"
	"xprem/config"
	"xprem/internal/handlers"
	"xprem/internal/services"

	"github.com/gorilla/mux"
)

// AppResolverMiddleware extracts APP_ID from the path vars, short-circuits a
// malformed or unregistered id with a 4xx, and hands the loaded app to the
// handler through the context.
func AppResolverMiddleware(appRepository services.AppRepository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			appId := mux.Vars(r)["APP_ID"]
			if !isValidAppID(appId) {
				handlers.RenderError(w, http.StatusBadRequest, "invalid app id")
				return
			}
			app, err := appRepository.GetAppByID(r.Context(), appId)
			if err != nil {
				handlers.RenderError(w, http.StatusNotFound, "app not found")
				return
			}
			next.ServeHTTP(w, r.WithContext(services.WithApp(r.Context(), app)))
		})
	}
}

// isValidAppID delegates to config.ValidateAppId so rules cannot drift from
// boot-time validation.
func isValidAppID(id string) bool {
	return config.ValidateAppId(id, "appId") == nil
}
