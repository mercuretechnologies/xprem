package mcp

import (
	"context"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/version"
	"net/http"
	"time"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ConfigureServer populates one session's server for its principal; the tool
// packages provide these, the composition root passes them in.
type ConfigureServer func(ctx context.Context, principal *services.DashboardPrincipal, server *mcpprot.Server)

type MCPService struct {
	streamable *mcpprot.StreamableHTTPHandler
}

func NewMCPService(configurators ...ConfigureServer) *MCPService {
	// One server per session, built at initialize: the tool list is per
	// account, so what a session's tools/list shows is already filtered to
	// what its principal may use.
	newSessionServer := func(req *http.Request) *mcpprot.Server {
		server := mcpprot.NewServer(&mcpprot.Implementation{
			Name:    "Expo-Open-Ota",
			Version: version.Version,
			Title:   "Expo Open OTA",
		}, nil)
		principal := services.PrincipalFromContext(req.Context())
		for _, configure := range configurators {
			configure(req.Context(), principal, server)
		}
		return server
	}

	streamable := mcpprot.NewStreamableHTTPHandler(
		newSessionServer,
		&mcpprot.StreamableHTTPOptions{
			// The SDK's DNS-rebinding guard 403s any loopback connection
			// carrying a public Host header, which is exactly a reverse proxy
			// forwarding to 127.0.0.1. It exists for unauthenticated local
			// servers; /mcp requires a Bearer before this handler runs.
			DisableLocalhostProtection: true,
			// MCP clients rarely DELETE their session; without a timeout,
			// sessions of vanished clients accumulate for the process
			// lifetime. An idle client past this simply re-initializes.
			SessionTimeout: 30 * time.Minute,
		},
	)
	return &MCPService{streamable: streamable}
}
