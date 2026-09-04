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

// mcpClaims is the claim set of both MCP OAuth JWTs. Type and ID are only set
// on the refresh token, Audience and Scope only on the access token.
type mcpClaims struct {
	jwt.RegisteredClaims
	Type           string `json:"type,omitempty"`
	UserID         string `json:"userId"`
	SessionVersion int32  `json:"sv"`
	Scope          string `json:"scope,omitempty"`
}

// IssueAccessToken mints the Bearer token the token endpoint hands out.
func (s *OAuthService) IssueAccessToken(principal services.DashboardPrincipal) (string, error) {
	token, err := crypto.GenerateJWTToken(s.secret, mcpClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   mcpSubject,
			Audience:  jwt.ClaimStrings{ResourceURL()},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(accessTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		UserID:         principal.UserId,
		SessionVersion: principal.SessionVersion,
		Scope:          ScopeMCP,
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
	claims := mcpClaims{}
	if _, err := crypto.DecodeAndExtractJWTToken(s.secret, tokenString, &claims); err != nil {
		return nil, ErrInvalidGrant
	}
	if claims.Subject != mcpSubject || claims.Type != "refreshToken" {
		return nil, ErrInvalidGrant
	}
	if claims.UserID == "" {
		return nil, ErrInvalidGrant
	}
	principal, err := s.principalForUser(ctx, claims.UserID)
	if err != nil {
		return nil, err
	}
	if principal.SessionVersion != claims.SessionVersion {
		return nil, ErrInvalidGrant
	}
	spentId := claims.ID
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
	refreshToken, err := crypto.GenerateJWTToken(s.secret, mcpClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   mcpSubject,
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        tokenId,
		},
		Type:           "refreshToken",
		UserID:         principal.UserId,
		SessionVersion: principal.SessionVersion,
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
	claims := mcpClaims{}
	if _, err := crypto.DecodeAndExtractJWTToken(s.secret, tokenString, &claims); err != nil {
		return nil, time.Time{}, err
	}
	if claims.Subject != mcpSubject {
		return nil, time.Time{}, errors.New("invalid token subject")
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != ResourceURL() {
		return nil, time.Time{}, errors.New("invalid token audience")
	}
	if claims.ExpiresAt == nil {
		return nil, time.Time{}, errors.New("invalid token expiration")
	}
	expiresAt := claims.ExpiresAt
	if claims.UserID == "" {
		return nil, time.Time{}, services.ErrSessionRevoked
	}

	user, err := s.userRepo.GetUserByID(ctx, claims.UserID)
	if err != nil {
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			return nil, time.Time{}, services.ErrSessionRevoked
		}
		return nil, time.Time{}, fmt.Errorf("%w: %v", services.ErrAuthUnavailable, err)
	}
	if !user.Enabled || user.SessionVersion != claims.SessionVersion {
		return nil, time.Time{}, services.ErrSessionRevoked
	}
	return &services.DashboardPrincipal{
		UserId:         user.Id,
		Email:          user.Email,
		IsAdmin:        user.IsAdmin,
		SessionVersion: user.SessionVersion,
	}, expiresAt.Time, nil
}
