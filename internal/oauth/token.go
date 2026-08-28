package oauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"time"
	"xprem/config"
	"xprem/internal/crypto"
	"xprem/internal/services"
	"xprem/internal/store"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// mcpSubject is what separates an MCP access token from every other JWT this
// server signs with the same secret; the dashboard tokens carry their own sub
// and each side's validation refuses the other's.
const mcpSubject = "mcp"

const (
	accessTokenTTL  = time.Hour
	refreshTokenTTL = 7 * 24 * time.Hour
	// refreshReplayGrace covers a client racing itself; same semantics as the
	// dashboard ledger.
	refreshReplayGrace = 30 * time.Second
)

// ErrInvalidGrant covers every way a code or refresh token can be refused;
// the token endpoint answers all of them with the RFC 6749 invalid_grant so
// a probe learns nothing about which check failed.
var ErrInvalidGrant = errors.New("invalid grant")

// ResourceURL is the RFC 8707 identifier of the MCP server, carried as the
// aud claim of every access token and required back at verification.
func ResourceURL() string {
	return config.BaseURL() + "/mcp"
}

// ResourceMetadataURL is where a 401 sends clients to discover how to
// authenticate (RFC 9728).
func ResourceMetadataURL() string {
	return config.BaseURL() + "/.well-known/oauth-protected-resource/mcp"
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

// TokenResponse is what the token endpoint returns for both grants.
type TokenResponse struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
	Scope        string
}

// ExchangeAuthorizationCodeRequest is the authorization_code grant as sent by
// the client.
type ExchangeAuthorizationCodeRequest struct {
	Code         string
	ClientID     string
	RedirectURI  string
	CodeVerifier string
	Resource     string
}

// ExchangeAuthorizationCode trades a consented code for a token pair. The code
// is consumed before anything is verified: a failed exchange must burn it, or
// its PKCE challenge could be brute-forced by retrying.
func (s *OAuthService) ExchangeAuthorizationCode(ctx context.Context, req ExchangeAuthorizationCodeRequest) (*TokenResponse, error) {
	if _, err := uuid.Parse(req.Code); err != nil {
		return nil, ErrInvalidGrant
	}
	if length := len(req.CodeVerifier); length < 43 || length > 128 {
		return nil, ErrInvalidGrant
	}
	code, err := s.codeRepo.ConsumeOAuthAuthorizationCode(ctx, req.Code)
	if err != nil {
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			return nil, ErrInvalidGrant
		}
		return nil, fmt.Errorf("%w: %v", services.ErrAuthUnavailable, err)
	}
	// The exchange must replay exactly what the user consented to.
	if code.ClientID != req.ClientID || code.RedirectURI != req.RedirectURI {
		return nil, ErrInvalidGrant
	}
	if req.Resource != "" && req.Resource != ResourceURL() {
		return nil, ErrInvalidGrant
	}
	digest := sha256.Sum256([]byte(req.CodeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(code.CodeChallenge)) != 1 {
		return nil, ErrInvalidGrant
	}

	principal, err := s.principalForUser(ctx, code.UserID)
	if err != nil {
		return nil, err
	}
	tokenId := uuid.New().String()
	expiresAt := time.Now().Add(refreshTokenTTL)
	if err := s.refreshRepo.InsertRefreshToken(ctx, store.InsertRefreshTokenParameters{
		ID:        tokenId,
		UserID:    principal.UserId,
		FamilyID:  uuid.New().String(),
		ExpiresAt: expiresAt,
	}); err != nil {
		return nil, fmt.Errorf("%w: %v", services.ErrAuthUnavailable, err)
	}
	if purgeErr := s.refreshRepo.DeleteExpiredRefreshTokens(ctx, principal.UserId); purgeErr != nil {
		log.Printf("failed to purge expired refresh tokens for user %s: %v", principal.UserId, purgeErr)
	}
	return s.issueTokenPair(*principal, tokenId, expiresAt)
}

// RefreshAccessToken trades a refresh token for a new pair and spends the old
// one; rotation, replay grace and replay revocation mirror the dashboard's
// RefreshSession over the same ledger.
func (s *OAuthService) RefreshAccessToken(ctx context.Context, tokenString string) (*TokenResponse, error) {
	claims := jwt.MapClaims{}
	if _, err := crypto.DecodeAndExtractJWTToken(s.secret, tokenString, &claims); err != nil {
		return nil, ErrInvalidGrant
	}
	if claims["sub"] != mcpSubject || claims["type"] != "refreshToken" {
		return nil, ErrInvalidGrant
	}
	userId, _ := claims["userId"].(string)
	if userId == "" {
		return nil, ErrInvalidGrant
	}
	principal, err := s.principalForUser(ctx, userId)
	if err != nil {
		return nil, err
	}
	if sessionVersion, _ := claims["sv"].(float64); principal.SessionVersion != int32(sessionVersion) {
		return nil, ErrInvalidGrant
	}
	spentId, _ := claims["jti"].(string)
	if spentId == "" {
		return nil, ErrInvalidGrant
	}

	successorId := uuid.New().String()
	expiresAt := time.Now().Add(refreshTokenTTL)
	_, err = s.refreshRepo.RotateRefreshToken(ctx, store.RotateRefreshTokenParameters{
		OldID:     spentId,
		NewID:     successorId,
		ExpiresAt: expiresAt,
	})
	if err == nil {
		if purgeErr := s.refreshRepo.DeleteExpiredRefreshTokens(ctx, principal.UserId); purgeErr != nil {
			log.Printf("failed to purge expired refresh tokens for user %s: %v", principal.UserId, purgeErr)
		}
		return s.issueTokenPair(*principal, successorId, expiresAt)
	}
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	if !errors.As(err, &notFoundErr) {
		return nil, fmt.Errorf("%w: %v", services.ErrAuthUnavailable, err)
	}

	// The rotation refused the token; find out why from its row.
	spent, err := s.refreshRepo.GetRefreshToken(ctx, spentId, refreshReplayGrace)
	if err != nil {
		if errors.As(err, &notFoundErr) {
			return nil, ErrInvalidGrant
		}
		return nil, fmt.Errorf("%w: %v", services.ErrAuthUnavailable, err)
	}
	if spent.UsedAt == nil {
		return nil, ErrInvalidGrant
	}
	if spent.UsedRecently && spent.ReplacedBy != nil {
		// Same client racing itself within refreshReplayGrace: hand back the
		// successor already written instead of minting a second one.
		successor, err := s.refreshRepo.GetRefreshToken(ctx, *spent.ReplacedBy, refreshReplayGrace)
		if err != nil {
			if errors.As(err, &notFoundErr) {
				return nil, ErrInvalidGrant
			}
			return nil, fmt.Errorf("%w: %v", services.ErrAuthUnavailable, err)
		}
		if successor.UsedAt != nil {
			return nil, ErrInvalidGrant
		}
		return s.issueTokenPair(*principal, successor.Id, successor.ExpiresAt)
	}

	// Spent long ago and presented again: a stolen credential. Bump the
	// account's generation to kill every session, not just this family.
	if err := s.refreshRepo.DeleteRefreshTokenFamily(ctx, spent.FamilyId); err != nil {
		log.Printf("failed to revoke oauth refresh token family %s after a replay: %v", spent.FamilyId, err)
	}
	if err := s.userRepo.BumpUserSessionVersion(ctx, principal.UserId); err != nil {
		log.Printf("failed to revoke the sessions of user %s after a replay: %v", principal.UserId, err)
	}
	log.Printf("🚨 [OAUTH] refresh token replay for user %s: family %s revoked, every session of the account invalidated", spent.UserId, spent.FamilyId)
	return nil, ErrInvalidGrant
}

// principalForUser re-reads the account; every grant dies with it.
func (s *OAuthService) principalForUser(ctx context.Context, userId string) (*services.DashboardPrincipal, error) {
	user, err := s.userRepo.GetUserByID(ctx, userId)
	if err != nil {
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			return nil, ErrInvalidGrant
		}
		return nil, fmt.Errorf("%w: %v", services.ErrAuthUnavailable, err)
	}
	if !user.Enabled {
		return nil, ErrInvalidGrant
	}
	return &services.DashboardPrincipal{
		UserId:         user.Id,
		Email:          user.Email,
		IsAdmin:        user.IsAdmin,
		SessionVersion: user.SessionVersion,
	}, nil
}

func (s *OAuthService) issueTokenPair(principal services.DashboardPrincipal, tokenId string, expiresAt time.Time) (*TokenResponse, error) {
	accessToken, err := s.IssueAccessToken(principal)
	if err != nil {
		return nil, err
	}
	refreshToken, err := crypto.GenerateJWTToken(s.secret, jwt.MapClaims{
		"sub":    mcpSubject,
		"type":   "refreshToken",
		"exp":    expiresAt.Unix(),
		"iat":    time.Now().Unix(),
		"userId": principal.UserId,
		"sv":     principal.SessionVersion,
		"jti":    tokenId,
	})
	if err != nil {
		return nil, fmt.Errorf("error while generating the mcp refresh token: %w", err)
	}
	return &TokenResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(accessTokenTTL.Seconds()),
		Scope:        ScopeMCP,
	}, nil
}

// AuthenticateMCPToken validates an access token and re-reads the account
// behind it, mirroring what AuthenticateSession does for dashboard JWTs: the
// signature proves the token was valid when minted, the user row says whether
// it still is. The returned time is the token's expiration.
func (s *OAuthService) AuthenticateMCPToken(ctx context.Context, tokenString string) (*services.DashboardPrincipal, time.Time, error) {
	claims := jwt.MapClaims{}
	if _, err := crypto.DecodeAndExtractJWTToken(s.secret, tokenString, &claims); err != nil {
		return nil, time.Time{}, err
	}
	if claims["sub"] != mcpSubject {
		return nil, time.Time{}, errors.New("invalid token subject")
	}
	if audience, _ := claims.GetAudience(); len(audience) != 1 || audience[0] != ResourceURL() {
		return nil, time.Time{}, errors.New("invalid token audience")
	}
	expiresAt, err := claims.GetExpirationTime()
	if err != nil || expiresAt == nil {
		return nil, time.Time{}, errors.New("invalid token expiration")
	}
	userId, _ := claims["userId"].(string)
	if userId == "" {
		return nil, time.Time{}, services.ErrSessionRevoked
	}
	sessionVersion, _ := claims["sv"].(float64)

	user, err := s.userRepo.GetUserByID(ctx, userId)
	if err != nil {
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			return nil, time.Time{}, services.ErrSessionRevoked
		}
		return nil, time.Time{}, fmt.Errorf("%w: %v", services.ErrAuthUnavailable, err)
	}
	if !user.Enabled || user.SessionVersion != int32(sessionVersion) {
		return nil, time.Time{}, services.ErrSessionRevoked
	}
	return &services.DashboardPrincipal{
		UserId:         user.Id,
		Email:          user.Email,
		IsAdmin:        user.IsAdmin,
		SessionVersion: user.SessionVersion,
	}, expiresAt.Time, nil
}
