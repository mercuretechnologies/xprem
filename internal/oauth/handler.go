package oauth

import (
	"encoding/json"
	"errors"
	"expo-open-ota/config"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/ratelimit"
	"net/http"
	"strings"
	"time"
)

// The single coarse scope this server issues; finer scopes wait until the MCP
// server exposes tools worth splitting over.
const ScopeMCP = "mcp"

type OAuthHandler struct {
	service *OAuthService
	limiter *ratelimit.Limiter
}

func NewOAuthHandler(service *OAuthService, limiter *ratelimit.Limiter) *OAuthHandler {
	return &OAuthHandler{service: service, limiter: limiter}
}

func baseURL() string {
	return strings.TrimRight(config.GetEnv("BASE_URL"), "/")
}

// WithCORS opens an endpoint to cross-origin callers. The OAuth surface is
// meant to be reached from other origins (claude.ai and its kind fetch the
// metadata and token endpoints from their own pages), and nothing on it relies
// on cookies, so a wildcard grants nothing a curl could not already do.
func WithCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, mcp-protocol-version")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// ProtectedResourceMetadataHandler serves RFC 9728 metadata for the MCP
// resource. Registered both path-inserted (/.well-known/...(/mcp) and at the
// root, because non-conforming clients probe either.
func (h *OAuthHandler) ProtectedResourceMetadataHandler(w http.ResponseWriter, r *http.Request) {
	base := baseURL()
	handlers.RenderJSON(w, http.StatusOK, map[string]interface{}{
		"resource":                 base + "/mcp",
		"authorization_servers":    []string{base},
		"scopes_supported":         []string{ScopeMCP},
		"bearer_methods_supported": []string{"header"},
	})
}

// AuthorizationServerMetadataHandler serves RFC 8414 metadata. The issuer is
// the bare BASE_URL, deliberately without a path: path-bearing issuers trigger
// the well-known path-insertion rules clients disagree on.
func (h *OAuthHandler) AuthorizationServerMetadataHandler(w http.ResponseWriter, r *http.Request) {
	base := baseURL()
	handlers.RenderJSON(w, http.StatusOK, map[string]interface{}{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{ScopeMCP},
	})
}

// registerRequest is the RFC 7591 metadata this server acts on; other fields
// are accepted and ignored.
type registerRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// RegisterHandler implements dynamic client registration (RFC 7591).
func (h *OAuthHandler) RegisterHandler(w http.ResponseWriter, r *http.Request) {
	clientIP := helpers.ClientIP(r)
	if decision := h.limiter.CheckOAuthRegister(clientIP); !decision.Allowed {
		handlers.RenderThrottled(w, decision.RetryAfter)
		return
	}

	var req registerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		renderRegistrationError(w, "request body is not valid JSON")
		return
	}
	// Public clients only: there is no secret to hand out, so a client asking
	// to authenticate with one registered something this server cannot honor.
	if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
		renderRegistrationError(w, "only token_endpoint_auth_method \"none\" is supported")
		return
	}

	client, err := h.service.RegisterClient(r.Context(), req.ClientName, req.RedirectURIs)
	if err != nil {
		if errors.Is(err, ErrInvalidClientMetadata) {
			renderRegistrationError(w, err.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "Could not register the client, try again later")
		return
	}
	h.limiter.RecordOAuthRegister(clientIP)

	handlers.RenderJSON(w, http.StatusCreated, map[string]interface{}{
		"client_id":                  client.ID,
		"client_name":                client.Name,
		"redirect_uris":              client.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"client_id_issued_at":        time.Now().Unix(),
	})
}

// renderRegistrationError uses the RFC 7591 error shape, which registering
// clients parse, instead of this server's own error envelope.
func renderRegistrationError(w http.ResponseWriter, description string) {
	handlers.RenderJSON(w, http.StatusBadRequest, map[string]string{
		"error":             "invalid_client_metadata",
		"error_description": description,
	})
}
