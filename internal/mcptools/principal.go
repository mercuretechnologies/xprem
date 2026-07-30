package mcptools

import (
	"context"
	"errors"
	"expo-open-ota/internal/services"
	"log"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// PrincipalFromRequest resolves who this tool call acts as; exported for the
// ee tool package. The handler's ctx derives from the session, not from the
// HTTP request, so the per-request identity travels in the request's
// TokenInfo, where the OAuth middleware's verifier stored it.
func PrincipalFromRequest(req *mcpprot.CallToolRequest) *services.DashboardPrincipal {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return nil
	}
	return services.PrincipalFromExtra(req.Extra.TokenInfo.Extra)
}

// errAppNotFound reads like a 404 on purpose, mirroring the rbac semantics: a
// member without access must not learn the app exists.
var errAppNotFound = errors.New("app not found")

// requireAppVisible gates the viewer-level app-scoped tools: any account that
// may see the app passes, others get the 404 answer.
func requireAppVisible(ctx context.Context, deps Deps, principal *services.DashboardPrincipal, appID string) error {
	if appID == "" {
		return errors.New("appId is required; list the apps with get_apps")
	}
	restricted, visible, err := deps.VisibleApps(ctx, principal)
	if err != nil {
		log.Printf("mcp tools could not resolve the visible apps of user %s: %v", principal.UserId, err)
		return errors.New("could not check the app access, try again later")
	}
	if restricted && !visible[appID] {
		return errAppNotFound
	}
	return nil
}
