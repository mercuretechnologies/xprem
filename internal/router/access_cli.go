package infrastructure

import (
	"expo-open-ota/ee/apikeyrestrictions"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/services"
	"net/http"
)

// authorizeCliRequest runs the enterprise access decision for a CLI request,
// writing the response and returning false when the request is refused.
func authorizeCliRequest(
	policy cliAccessPolicy,
	w http.ResponseWriter,
	r *http.Request,
	credential services.CliCredential,
	action apikeyrestrictions.Action,
	branchName string,
) bool {
	if branchName == "" {
		handlers.RenderError(w, http.StatusForbidden, "This route requires a branch")
		return false
	}
	// KeyID 0 is stateless mode: no API key exists to carry access rules.
	if credential.KeyID != 0 {
		err := policy.Authorize(r.Context(), apikeyrestrictions.CliRequest{
			AppID:    credential.AppID,
			APIKeyID: credential.KeyID,
			Branch:   branchName,
			Action:   action,
			ClientIP: helpers.ClientIP(r),
		})
		if err != nil {
			handlers.RenderCliAuthError(w, err)
			return false
		}
	}
	return true
}
