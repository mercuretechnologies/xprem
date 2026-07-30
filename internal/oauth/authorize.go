package oauth

import (
	"context"
	"errors"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"
	"fmt"
	"net/url"
	"slices"
	"time"

	"github.com/google/uuid"
)

// authorizationCodeTTL only covers the redirect from the consent click to the
// token exchange; login happens before the code is minted.
const authorizationCodeTTL = time.Minute

// ErrInvalidAuthorizationRequest rejects an authorization request; its message
// is safe to show to the user.
var ErrInvalidAuthorizationRequest = errors.New("invalid authorization request")

// AuthorizationRequest is the query an OAuth client sends to the authorize
// endpoint, echoed through the consent screen and back.
type AuthorizationRequest struct {
	ClientID            string
	RedirectURI         string
	ResponseType        string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	State               string
	Resource            string
}

// ValidateAuthorizationRequest checks a request against the registered client
// and this server's capabilities, and returns the client for display. Nothing
// is ever redirected to a URI this validation has not approved.
func (s *OAuthService) ValidateAuthorizationRequest(ctx context.Context, req AuthorizationRequest) (Client, error) {
	if _, err := uuid.Parse(req.ClientID); err != nil {
		return Client{}, fmt.Errorf("%w: unknown client_id", ErrInvalidAuthorizationRequest)
	}
	stored, err := s.clientRepo.GetOAuthClient(ctx, req.ClientID)
	if err != nil {
		if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
			return Client{}, fmt.Errorf("%w: unknown client_id", ErrInvalidAuthorizationRequest)
		}
		return Client{}, fmt.Errorf("%w: %v", services.ErrAuthUnavailable, err)
	}
	client := Client{ID: stored.Id, Name: stored.Name, RedirectURIs: stored.RedirectURIs}
	if !slices.Contains(client.RedirectURIs, req.RedirectURI) {
		return Client{}, fmt.Errorf("%w: redirect_uri is not registered for this client", ErrInvalidAuthorizationRequest)
	}
	if req.ResponseType != "code" {
		return Client{}, fmt.Errorf("%w: only response_type \"code\" is supported", ErrInvalidAuthorizationRequest)
	}
	if req.CodeChallengeMethod != "S256" {
		return Client{}, fmt.Errorf("%w: only code_challenge_method \"S256\" is supported (PKCE is required)", ErrInvalidAuthorizationRequest)
	}
	// 43-128 chars per RFC 7636; anything else is not a S256 challenge.
	if length := len(req.CodeChallenge); length < 43 || length > 128 {
		return Client{}, fmt.Errorf("%w: code_challenge is malformed", ErrInvalidAuthorizationRequest)
	}
	if req.Scope != "" && req.Scope != ScopeMCP {
		return Client{}, fmt.Errorf("%w: unsupported scope %q", ErrInvalidAuthorizationRequest, req.Scope)
	}
	if req.Resource != "" && req.Resource != ResourceURL() {
		return Client{}, fmt.Errorf("%w: unknown resource %q", ErrInvalidAuthorizationRequest, req.Resource)
	}
	return client, nil
}

// CreateAuthorizationCode turns an approved consent into a single-use code and
// returns the full redirect URL delivering it to the client.
func (s *OAuthService) CreateAuthorizationCode(ctx context.Context, userId string, req AuthorizationRequest) (string, error) {
	if _, err := s.ValidateAuthorizationRequest(ctx, req); err != nil {
		return "", err
	}
	code := uuid.New().String()
	if err := s.codeRepo.DeleteExpiredOAuthAuthorizationCodes(ctx); err != nil {
		return "", err
	}
	if err := s.codeRepo.InsertOAuthAuthorizationCode(ctx, store.InsertOAuthAuthorizationCodeParameters{
		ID:            code,
		ClientID:      req.ClientID,
		UserID:        userId,
		RedirectURI:   req.RedirectURI,
		CodeChallenge: req.CodeChallenge,
		Scope:         ScopeMCP,
		ExpiresAt:     time.Now().Add(authorizationCodeTTL),
	}); err != nil {
		return "", err
	}

	// The URI parses: it survived registration validation and the exact-match
	// check above.
	redirect, err := url.Parse(req.RedirectURI)
	if err != nil {
		return "", err
	}
	query := redirect.Query()
	query.Set("code", code)
	if req.State != "" {
		query.Set("state", req.State)
	}
	redirect.RawQuery = query.Encode()
	return redirect.String(), nil
}
