package middleware

import (
	"context"
	"errors"
	"expo-open-ota/internal/oauth"
	"expo-open-ota/internal/services"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

// NewOAuthMiddleware guards the MCP endpoint behind an OAuth access token.
// It wraps the SDK's RequireBearerToken rather than checking the header
// itself: that is the only way to plant the TokenInfo the streamable
// transport pins sessions to (session hijacking prevention), and it also
// answers refusals with the WWW-Authenticate challenge clients discover the
// authorization flow through.
func NewOAuthMiddleware(oauthService *oauth.OAuthService) mux.MiddlewareFunc {
	verifier := func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		principal, expiresAt, err := oauthService.AuthenticateMCPToken(ctx, token)
		if err != nil {
			// A database outage must not read as a dead token (500, not
			// 401), the client would throw away a valid credential and
			// restart the flow. Bare errors on both paths: the SDK writes
			// the error message into the response body.
			if errors.Is(err, services.ErrAuthUnavailable) {
				return nil, services.ErrAuthUnavailable
			}
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{
			UserID:     principal.UserId,
			Scopes:     []string{oauth.ScopeMCP},
			Expiration: expiresAt,
			Extra:      map[string]any{services.PrincipalExtraKey: principal},
		}, nil
	}
	requireBearer := auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: oauth.ResourceMetadataURL(),
	})
	return func(next http.Handler) http.Handler {
		return requireBearer(principalFromTokenInfo(next))
	}
}

// principalFromTokenInfo copies the principal minted by the verifier into the
// context key the rest of the codebase reads.
func principalFromTokenInfo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var principal *services.DashboardPrincipal
		if info := auth.TokenInfoFromContext(r.Context()); info != nil {
			principal = services.PrincipalFromExtra(info.Extra)
		}
		if principal == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(services.WithPrincipal(r.Context(), principal)))
	})
}
