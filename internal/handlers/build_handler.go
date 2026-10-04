package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"xprem/internal/providers/appstoreconnect"
	"xprem/internal/repository"
	"xprem/internal/services"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/gorilla/mux"
)

type BuildHandler struct {
	// authorizeEnvironment judges the environment a request resolved to; nil allows every environment.
	authorizeEnvironment func(r *http.Request, environment string) error
	environments         *services.EnvironmentService
	credentials          *services.CredentialsService
	iosCredentials       *services.IosCredentialsService
	identifiers          *services.AppIdentifierService
}

func NewBuildHandler(environments *services.EnvironmentService, credentials *services.CredentialsService, iosCredentials *services.IosCredentialsService, identifiers *services.AppIdentifierService) *BuildHandler {
	return &BuildHandler{environments: environments, credentials: credentials, iosCredentials: iosCredentials, identifiers: identifiers}
}

// SetEnvironmentAuthorizer plugs the per-key environment access check. Nil-safe.
func (h *BuildHandler) SetEnvironmentAuthorizer(authorize func(r *http.Request, environment string) error) {
	h.authorizeEnvironment = authorize
}

func RenderBuildInputError(w http.ResponseWriter, err error) {
	var missing *repository.ErrResourceNotFound
	switch {
	case errors.Is(err, repository.ErrBuildNumberExhausted):
		RenderError(w, http.StatusConflict, err.Error())
	case validation.IsValidationError(err):
		RenderError(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &missing):
		RenderError(w, http.StatusNotFound, missing.Error())
	case errors.Is(err, repository.ErrNotSupportedInStatelessMode):
		RenderError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, appstoreconnect.ErrUnavailable):
		RenderError(w, http.StatusBadGateway, "App Store Connect could not be reached. Try again in a few minutes.")
	default:
		RenderError(w, http.StatusInternalServerError, "Could not retrieve build inputs.")
	}
}

// renderBuildSecrets writes a build input export that must never be cached.
func renderBuildSecrets(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Cache-Control", "no-store")
	RenderJSON(w, http.StatusOK, payload)
}
func (h *BuildHandler) Environment(w http.ResponseWriter, r *http.Request) {
	query, err := buildEnvironmentQuery(r)
	if err != nil {
		RenderBuildInputError(w, err)
		return
	}
	var authorize func(environment string) error
	if h.authorizeEnvironment != nil {
		authorize = func(environment string) error { return h.authorizeEnvironment(r, environment) }
	}
	environment, err := h.environments.ExportVariables(r.Context(), mux.Vars(r)["APP_ID"], query["channel"], query["environment"], authorize)
	if errors.Is(err, services.ErrCliAccessDenied) || errors.Is(err, services.ErrCliAuthUnavailable) {
		RenderCliAuthError(w, err)
		return
	}
	if err != nil {
		RenderBuildInputError(w, err)
		return
	}
	renderBuildSecrets(w, environment)
}

// Reject ambiguous selectors rather than silently using the first query value.
func buildEnvironmentQuery(r *http.Request) (map[string]string, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, validation.Errorf("query", "invalid query parameters")
	}
	selectors := map[string]string{}
	for key, values := range query {
		if (key != "channel" && key != "environment") || len(values) != 1 || values[0] == "" {
			return nil, validation.Errorf("query", "expected a single non-empty channel or environment")
		}
		selectors[key] = values[0]
	}
	if len(selectors) > 1 {
		return nil, validation.Errorf("query", "channel and environment are mutually exclusive")
	}
	return selectors, nil
}

// Deliberate allowlist: never serialize the credential record or Google Play
// service account. encoding/json represents the complete file bytes as base64.
type AndroidBuildCredentials struct {
	Keystore         []byte `json:"keystore"`
	KeystorePassword string `json:"keystorePassword"`
	KeyAlias         string `json:"keyAlias"`
	KeyPassword      string `json:"keyPassword"`
}

func (h *BuildHandler) AndroidCredentials(w http.ResponseWriter, r *http.Request) {
	identifierID := services.BuildIdentifierFromContext(r.Context())
	if identifierID == "" {
		RenderCliAuthError(w, services.ErrUnauthorized)
		return
	}
	exported, err := h.credentials.ExportAndroidKeystore(r.Context(), mux.Vars(r)["APP_ID"], identifierID)
	if err != nil {
		RenderBuildInputError(w, err)
		return
	}
	renderBuildSecrets(w, AndroidBuildCredentials{Keystore: exported.Keystore, KeystorePassword: exported.KeystorePassword, KeyAlias: exported.KeyAlias, KeyPassword: exported.KeyPassword})
}

// IosBuildCredentials is the allowlist of what signs one iOS build; encoding/json represents the files as base64.
type IosBuildCredentials struct {
	CertificateP12      []byte `json:"certificateP12"`
	CertificatePassword string `json:"certificatePassword"`
	ProvisioningProfile []byte `json:"provisioningProfile"`
	TeamID              string `json:"teamId"`
}

func (h *BuildHandler) IosCredentials(w http.ResponseWriter, r *http.Request) {
	identifierID := services.BuildIdentifierFromContext(r.Context())
	if identifierID == "" {
		RenderCliAuthError(w, services.ErrUnauthorized)
		return
	}
	distribution := types.IosDistribution(r.URL.Query().Get("distribution"))
	prepared, err := h.iosCredentials.PrepareIosBuildCredentials(r.Context(), mux.Vars(r)["APP_ID"], identifierID, distribution)
	if err != nil {
		RenderBuildInputError(w, err)
		return
	}
	renderBuildSecrets(w, IosBuildCredentials{CertificateP12: prepared.CertificateP12, CertificatePassword: prepared.CertificatePassword, ProvisioningProfile: prepared.ProvisioningProfile, TeamID: prepared.TeamID})
}

func (h *BuildHandler) AllocateBuildNumber(w http.ResponseWriter, r *http.Request) {
	identifierID := services.BuildIdentifierFromContext(r.Context())
	if identifierID == "" {
		RenderCliAuthError(w, services.ErrUnauthorized)
		return
	}
	buildNumber, err := h.identifiers.AllocateBuildNumber(r.Context(), mux.Vars(r)["APP_ID"], identifierID)
	if err != nil {
		RenderBuildInputError(w, err)
		return
	}
	RenderJSON(w, http.StatusOK, map[string]any{"buildNumber": buildNumber})
}

func (h *BuildHandler) ResolveIdentifier(w http.ResponseWriter, r *http.Request) {
	identifierID := services.BuildIdentifierFromContext(r.Context())
	if identifierID == "" {
		RenderCliAuthError(w, services.ErrUnauthorized)
		return
	}
	RenderJSON(w, http.StatusOK, map[string]string{"identifierId": identifierID})
}
