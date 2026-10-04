package infrastructure

import (
	"context"
	"net/http"
	"xprem/ee/apikeyrestrictions"
	"xprem/internal/handlers"
	"xprem/internal/helpers"
	"xprem/internal/repository"
	"xprem/internal/services"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type buildAccessPolicy interface {
	AuthorizeBuild(context.Context, apikeyrestrictions.BuildRequest) error
	AuthorizeEnvironment(context.Context, apikeyrestrictions.EnvironmentRequest) error
}

// environmentAuthorizer judges the environment a build request resolved to with the key its guard
// authenticated. A channel only names its environment once resolved, so this runs inside the export.
func environmentAuthorizer(policy buildAccessPolicy) func(r *http.Request, environment string) error {
	return func(r *http.Request, environment string) error {
		credential := services.CliAuthFromContext(r.Context())
		if credential == nil {
			return services.ErrUnauthorized
		}
		return policy.AuthorizeEnvironment(r.Context(), apikeyrestrictions.EnvironmentRequest{
			APIKeyContext: apikeyrestrictions.APIKeyContext{AppID: credential.AppID, APIKeyID: credential.KeyID, ClientIP: helpers.ClientIP(r)},
			Environment:   environment,
		})
	}
}

type buildGroup struct {
	router       *mux.Router
	cliAuth      *services.CliAuthService
	apiKeyAccess buildAccessPolicy
	identifiers  services.AppIdentifierRepository
}

// route applies the action declared by each build-input endpoint.
func (g buildGroup) route(method, path string, handler http.HandlerFunc, action apikeyrestrictions.BuildAction) {
	g.router.Handle(path, g.guard(action)(handler)).Methods(method)
}

func (g buildGroup) guard(action apikeyrestrictions.BuildAction) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			vars := mux.Vars(r)
			credential, err := g.cliAuth.AuthenticateCliCredential(r.Context(), vars["APP_ID"], helpers.GetAuth(r))
			if err != nil {
				handlers.RenderCliAuthError(w, err)
				return
			}
			// Build requires a registered app token; Expo stateless credentials have no key ID.
			if credential.KeyID <= 0 {
				handlers.RenderCliAuthError(w, services.ErrUnauthorized)
				return
			}
			if g.identifiers == nil {
				handlers.RenderBuildInputError(w, repository.ErrNotSupportedInStatelessMode)
				return
			}
			var ref *repository.AppIdentifierRef
			target := vars["APPLICATION_ID"]
			if target != "" {
				platform, parseErr := types.ParsePlatform(vars["PLATFORM"])
				if parseErr != nil {
					handlers.RenderBuildInputError(w, validation.Errorf("platform", "%s", parseErr))
					return
				}
				ref, err = g.identifiers.GetAppIdentifierByPlatformAndIdentifier(r.Context(), credential.AppID, platform, target)
			} else {
				identifierID, parseErr := uuid.Parse(vars["IDENTIFIER_ID"])
				if parseErr != nil {
					handlers.RenderBuildInputError(w, validation.Errorf("identifierId", "invalid identifier id"))
					return
				}
				target = identifierID.String()
				ref, err = g.identifiers.GetAppIdentifierByID(r.Context(), credential.AppID, target)
			}
			if err != nil {
				handlers.RenderBuildInputError(w, err)
				return
			}
			if ref == nil {
				handlers.RenderBuildInputError(w, &repository.ErrResourceNotFound{Resource: "app identifier", Identifier: target})
				return
			}
			err = g.apiKeyAccess.AuthorizeBuild(r.Context(), apikeyrestrictions.BuildRequest{
				APIKeyContext:   apikeyrestrictions.APIKeyContext{AppID: credential.AppID, APIKeyID: credential.KeyID, ClientIP: helpers.ClientIP(r)},
				AppIdentifierID: ref.Id,
				Action:          action,
			})
			if err != nil {
				handlers.RenderCliAuthError(w, err)
				return
			}
			ctx := services.WithCliAuth(r.Context(), credential)
			ctx = services.WithBuildIdentifier(ctx, ref.Id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
