package oauth

import (
	"context"
	"errors"
	"expo-open-ota/internal/crypto"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// mcpSubject is what separates an MCP access token from every other JWT this
// server signs with the same secret; the dashboard tokens carry their own sub
// and each side's validation refuses the other's.
const mcpSubject = "mcp"

const accessTokenTTL = time.Hour

// ResourceURL is the RFC 8707 identifier of the MCP server, carried as the
// aud claim of every access token and required back at verification.
func ResourceURL() string {
	return baseURL() + "/mcp"
}

// ResourceMetadataURL is where a 401 sends clients to discover how to
// authenticate (RFC 9728).
func ResourceMetadataURL() string {
	return baseURL() + "/.well-known/oauth-protected-resource/mcp"
}

// IssueAccessToken mints the Bearer token the token endpoint hands out.
func (s *OAuthService) IssueAccessToken(principal services.DashboardPrincipal) (string, error) {
	token, err := crypto.GenerateJWTToken(s.secret, jwt.MapClaims{
		"sub":    mcpSubject,
		"aud":    ResourceURL(),
		"exp":    time.Now().Add(accessTokenTTL).Unix(),
		"iat":    time.Now().Unix(),
		"userId": principal.UserId,
		"sv":     principal.SessionVersion,
		"scope":  ScopeMCP,
	})
	if err != nil {
		return "", fmt.Errorf("error while generating the mcp access token: %w", err)
	}
	return token, nil
}

// AuthenticateMCPToken validates an access token and re-reads the account
// behind it, mirroring what AuthenticateSession does for dashboard JWTs: the
// signature proves the token was valid when minted, the user row says whether
// it still is.
func (s *OAuthService) AuthenticateMCPToken(ctx context.Context, tokenString string) (*services.DashboardPrincipal, error) {
	claims := jwt.MapClaims{}
	if _, err := crypto.DecodeAndExtractJWTToken(s.secret, tokenString, &claims); err != nil {
		return nil, err
	}
	if claims["sub"] != mcpSubject {
		return nil, errors.New("invalid token subject")
	}
	if audience, _ := claims.GetAudience(); len(audience) != 1 || audience[0] != ResourceURL() {
		return nil, errors.New("invalid token audience")
	}
	userId, _ := claims["userId"].(string)
	if userId == "" {
		return nil, services.ErrSessionRevoked
	}
	sessionVersion, _ := claims["sv"].(float64)

	user, err := s.userRepo.GetUserByID(ctx, userId)
	if err != nil {
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			return nil, services.ErrSessionRevoked
		}
		return nil, fmt.Errorf("%w: %v", services.ErrAuthUnavailable, err)
	}
	if !user.Enabled || user.SessionVersion != int32(sessionVersion) {
		return nil, services.ErrSessionRevoked
	}
	return &services.DashboardPrincipal{
		UserId:         user.Id,
		Email:          user.Email,
		IsAdmin:        user.IsAdmin,
		SessionVersion: user.SessionVersion,
	}, nil
}
