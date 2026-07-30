package mcptools

import (
	"expo-open-ota/internal/services"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// principalFromRequest resolves who this tool call acts as. The handler's ctx
// derives from the session, not from the HTTP request, so the per-request
// identity travels in the request's TokenInfo, where the OAuth middleware's
// verifier stored it.
func principalFromRequest(req *mcpprot.CallToolRequest) *services.DashboardPrincipal {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return nil
	}
	return services.PrincipalFromExtra(req.Extra.TokenInfo.Extra)
}
