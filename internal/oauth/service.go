package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"xprem/config"
	"xprem/internal/services"
	"xprem/internal/store"

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
	GetOAuthClient(ctx context.Context, id string) (store.OAuthClient, error)
}

// CodeRepository is the authorization-code ledger.
type CodeRepository interface {
	InsertOAuthAuthorizationCode(ctx context.Context, params store.InsertOAuthAuthorizationCodeParameters) error
	ConsumeOAuthAuthorizationCode(ctx context.Context, id string) (store.OAuthAuthorizationCode, error)
	DeleteExpiredOAuthAuthorizationCodes(ctx context.Context) error
}

// UserRepository is the slice of the users table the token flows need.
type UserRepository interface {
	GetUserByID(ctx context.Context, id string) (store.User, error)
	// BumpUserSessionVersion is the replay response: it retires every session
	// of the account, dashboard included.
	BumpUserSessionVersion(ctx context.Context, id string) error
}

// Client is a registered OAuth client: a public client (no secret) pinned to
// the redirect URIs it declared at registration.
type Client struct {
	ID           string
	Name         string
	RedirectURIs []string
}

type OAuthService struct {
	clientRepo  ClientRepository
	codeRepo    CodeRepository
	refreshRepo services.RefreshTokenRepository
	userRepo    UserRepository
	secret      string
}

func NewOAuthService(clientRepo ClientRepository, codeRepo CodeRepository, refreshRepo services.RefreshTokenRepository, userRepo UserRepository) *OAuthService {
	return &OAuthService{
		clientRepo:  clientRepo,
		codeRepo:    codeRepo,
		refreshRepo: refreshRepo,
		userRepo:    userRepo,
		secret:      config.GetEnv("JWT_SECRET"),
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

// executableSchemes are the schemes a browser runs instead of navigating to.
// The consent screen hands the approved redirect_uri to location.assign, so one
// of these would execute on the dashboard origin.
var executableSchemes = map[string]bool{
	"javascript":  true,
	"data":        true,
	"vbscript":    true,
	"blob":        true,
	"file":        true,
	"about":       true,
	"filesystem":  true,
	"view-source": true,
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
	// url.Parse lowercases the scheme, so this matches JaVaScRiPt: too.
	if executableSchemes[u.Scheme] {
		return fmt.Errorf("%w: redirect_uri %q uses a scheme the browser executes", ErrInvalidClientMetadata, raw)
	}
	// Opaque is non-empty for scheme:payload forms; a redirect target is always
	// hierarchical (scheme://host/path or scheme:/path).
	if u.Opaque != "" {
		return fmt.Errorf("%w: redirect_uri %q is not a hierarchical URI", ErrInvalidClientMetadata, raw)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("%w: redirect_uri %q uses http on a non-loopback host", ErrInvalidClientMetadata, raw)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
