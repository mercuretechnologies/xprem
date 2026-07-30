package oauth

import (
	"encoding/json"
	"errors"
	"expo-open-ota/config"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/ratelimit"
	"expo-open-ota/internal/services"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// consentPath is the dashboard route the authorize endpoint bounces to; the
// SPA holds the session (localStorage), so only it can ask who is consenting.
const consentPath = "/dashboard/oauth/consent"

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
	handlers.RenderJSON(w, http.StatusOK, map[string]interface{}{
		"resource":                 ResourceURL(),
		"authorization_servers":    []string{baseURL()},
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

func authorizationRequestFromValues(values url.Values) AuthorizationRequest {
	return AuthorizationRequest{
		ClientID:            values.Get("client_id"),
		RedirectURI:         values.Get("redirect_uri"),
		ResponseType:        values.Get("response_type"),
		CodeChallenge:       values.Get("code_challenge"),
		CodeChallengeMethod: values.Get("code_challenge_method"),
		Scope:               values.Get("scope"),
		State:               values.Get("state"),
		Resource:            values.Get("resource"),
	}
}

// AuthorizeHandler validates an authorization request and bounces it to the
// consent screen. Invalid requests are answered in place, never redirected:
// the redirect_uri cannot be trusted before it is validated.
func (h *OAuthHandler) AuthorizeHandler(w http.ResponseWriter, r *http.Request) {
	req := authorizationRequestFromValues(r.URL.Query())
	client, err := h.service.ValidateAuthorizationRequest(r.Context(), req)
	if err != nil {
		if errors.Is(err, ErrInvalidAuthorizationRequest) {
			handlers.RenderError(w, http.StatusBadRequest, err.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "Could not validate the authorization request, try again later")
		return
	}

	consent := url.Values{}
	consent.Set("client_id", req.ClientID)
	// The registered name, resolved server-side for the consent screen; a
	// name in the query could be forged.
	consent.Set("client_name", client.Name)
	consent.Set("redirect_uri", req.RedirectURI)
	consent.Set("response_type", req.ResponseType)
	consent.Set("code_challenge", req.CodeChallenge)
	consent.Set("code_challenge_method", req.CodeChallengeMethod)
	if req.Scope != "" {
		consent.Set("scope", req.Scope)
	}
	if req.State != "" {
		consent.Set("state", req.State)
	}
	if req.Resource != "" {
		consent.Set("resource", req.Resource)
	}
	http.Redirect(w, r, baseURL()+consentPath+"?"+consent.Encode(), http.StatusFound)
}

// ConsentHandler turns an approved consent into an authorization code. It sits
// behind the dashboard auth middleware; the caller is the consent screen
// acting with the user's own session.
func (h *OAuthHandler) ConsentHandler(w http.ResponseWriter, r *http.Request) {
	principal := services.PrincipalFromContext(r.Context())
	if principal == nil {
		handlers.RenderError(w, http.StatusUnauthorized, "This action requires a dashboard session")
		return
	}
	if err := r.ParseForm(); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "invalid form body")
		return
	}
	var redirectURL string
	var err error
	switch r.PostForm.Get("decision") {
	case "approve":
		redirectURL, err = h.service.CreateAuthorizationCode(r.Context(), principal.UserId, authorizationRequestFromValues(r.PostForm))
	case "deny":
		redirectURL, err = h.service.DenyAuthorization(r.Context(), authorizationRequestFromValues(r.PostForm))
	default:
		handlers.RenderError(w, http.StatusBadRequest, "decision must be approve or deny")
		return
	}
	if err != nil {
		if errors.Is(err, ErrInvalidAuthorizationRequest) {
			handlers.RenderError(w, http.StatusBadRequest, err.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "Could not record the consent, try again later")
		return
	}
	handlers.RenderJSON(w, http.StatusOK, map[string]string{"redirectUrl": redirectURL})
}

// TokenHandler implements the token endpoint for the authorization_code and
// refresh_token grants (RFC 6749 request and error shapes).
func (h *OAuthHandler) TokenHandler(w http.ResponseWriter, r *http.Request) {
	// Tokens in the body: no cache anywhere.
	w.Header().Set("Cache-Control", "no-store")
	clientIP := helpers.ClientIP(r)
	if decision := h.limiter.CheckRefresh(clientIP); !decision.Allowed {
		handlers.RenderThrottled(w, decision.RetryAfter)
		return
	}
	if err := r.ParseForm(); err != nil {
		renderTokenError(w, "invalid_request", "request body is not a valid form")
		return
	}

	var response *TokenResponse
	var err error
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		response, err = h.service.ExchangeAuthorizationCode(r.Context(), ExchangeAuthorizationCodeRequest{
			Code:         r.PostForm.Get("code"),
			ClientID:     r.PostForm.Get("client_id"),
			RedirectURI:  r.PostForm.Get("redirect_uri"),
			CodeVerifier: r.PostForm.Get("code_verifier"),
			Resource:     r.PostForm.Get("resource"),
		})
	case "refresh_token":
		response, err = h.service.RefreshAccessToken(r.Context(), r.PostForm.Get("refresh_token"))
	default:
		renderTokenError(w, "unsupported_grant_type", "supported grant types are authorization_code and refresh_token")
		return
	}
	if err != nil {
		if errors.Is(err, ErrInvalidGrant) {
			h.limiter.RecordRefreshFailure(clientIP)
			renderTokenError(w, "invalid_grant", "the grant is invalid, expired, or was already used")
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "Could not process the token request, try again later")
		return
	}
	handlers.RenderJSON(w, http.StatusOK, map[string]interface{}{
		"access_token":  response.AccessToken,
		"token_type":    "Bearer",
		"expires_in":    response.ExpiresIn,
		"refresh_token": response.RefreshToken,
		"scope":         response.Scope,
	})
}

// renderTokenError uses the RFC 6749 error shape, which token clients parse.
func renderTokenError(w http.ResponseWriter, code string, description string) {
	handlers.RenderJSON(w, http.StatusBadRequest, map[string]string{
		"error":             code,
		"error_description": description,
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
