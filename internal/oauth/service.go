package oauth

import (
	"context"
	"errors"
	"expo-open-ota/config"
	"expo-open-ota/internal/store"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

const (
	maxRedirectURIs    = 10
	maxRedirectURILen  = 512
	maxClientNameLen   = 256
	fallbackClientName = "MCP client"
)

// ErrInvalidClientMetadata rejects a registration; its message is safe to
// return to the client as the RFC 7591 error_description.
var ErrInvalidClientMetadata = errors.New("invalid client metadata")

type ClientRepository interface {
	InsertOAuthClient(ctx context.Context, params store.InsertOAuthClientParameters) error
}

// UserRepository is the slice of the users table token verification needs.
type UserRepository interface {
	GetUserByID(ctx context.Context, id string) (store.User, error)
}

// Client is a registered OAuth client: a public client (no secret) pinned to
// the redirect URIs it declared at registration.
type Client struct {
	ID           string
	Name         string
	RedirectURIs []string
}

type OAuthService struct {
	clientRepo ClientRepository
	userRepo   UserRepository
	secret     string
}

func NewOAuthService(clientRepo ClientRepository, userRepo UserRepository) *OAuthService {
	return &OAuthService{
		clientRepo: clientRepo,
		userRepo:   userRepo,
		secret:     config.GetEnv("JWT_SECRET"),
	}
}

// RegisterClient validates the requested metadata and stores a new client.
// Errors wrapping ErrInvalidClientMetadata are the caller's fault; anything
// else is the database.
func (s *OAuthService) RegisterClient(ctx context.Context, name string, redirectURIs []string) (Client, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fallbackClientName
	}
	if len(name) > maxClientNameLen {
		return Client{}, fmt.Errorf("%w: client_name exceeds %d characters", ErrInvalidClientMetadata, maxClientNameLen)
	}
	if len(redirectURIs) == 0 {
		return Client{}, fmt.Errorf("%w: redirect_uris is required", ErrInvalidClientMetadata)
	}
	if len(redirectURIs) > maxRedirectURIs {
		return Client{}, fmt.Errorf("%w: at most %d redirect_uris are allowed", ErrInvalidClientMetadata, maxRedirectURIs)
	}
	for _, raw := range redirectURIs {
		if err := validateRedirectURI(raw); err != nil {
			return Client{}, err
		}
	}

	client := Client{
		ID:           uuid.New().String(),
		Name:         name,
		RedirectURIs: redirectURIs,
	}
	if err := s.clientRepo.InsertOAuthClient(ctx, store.InsertOAuthClientParameters{
		ID:           client.ID,
		Name:         client.Name,
		RedirectURIs: client.RedirectURIs,
	}); err != nil {
		return Client{}, err
	}
	return client, nil
}

// validateRedirectURI accepts https URIs, custom app schemes, and plain http
// only towards a loopback host, per the OAuth 2.1 rules for public clients.
func validateRedirectURI(raw string) error {
	if raw == "" || len(raw) > maxRedirectURILen {
		return fmt.Errorf("%w: redirect_uri is empty or exceeds %d characters", ErrInvalidClientMetadata, maxRedirectURILen)
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() {
		return fmt.Errorf("%w: redirect_uri %q is not an absolute URI", ErrInvalidClientMetadata, raw)
	}
	if u.Fragment != "" {
		return fmt.Errorf("%w: redirect_uri %q must not contain a fragment", ErrInvalidClientMetadata, raw)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("%w: redirect_uri %q uses http on a non-loopback host", ErrInvalidClientMetadata, raw)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
