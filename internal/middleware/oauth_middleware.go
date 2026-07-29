package middleware

import (
	"errors"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/oauth"
	"expo-open-ota/internal/services"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
)

// NewOAuthMiddleware guards the MCP endpoint behind an OAuth access token.
// Every refusal carries the WWW-Authenticate header pointing at the resource
// metadata; that header is how a client that has never seen this server
// discovers where to authenticate, so the 401 is part of the protocol, not
// just an error.
func NewOAuthMiddleware(oauthService *oauth.OAuthService) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bearerToken, err := helpers.GetBearerToken(r)
			if err != nil {
				renderUnauthorized(w, "")
				return
			}
			principal, err := oauthService.AuthenticateMCPToken(r.Context(), bearerToken)
			if err != nil {
				// A database outage must not read as a dead token, the client
				// would throw away a valid credential and restart the flow.
				if errors.Is(err, services.ErrAuthUnavailable) {
					http.Error(w, "Could not verify the account", http.StatusInternalServerError)
					return
				}
				renderUnauthorized(w, "invalid_token")
				return
			}
			next.ServeHTTP(w, r.WithContext(services.WithPrincipal(r.Context(), principal)))
		})
	}
}

func renderUnauthorized(w http.ResponseWriter, errorCode string) {
	challenge := fmt.Sprintf("Bearer resource_metadata=%q", oauth.ResourceMetadataURL())
	if errorCode != "" {
		challenge += fmt.Sprintf(", error=%q", errorCode)
	}
	w.Header().Set("WWW-Authenticate", challenge)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}
