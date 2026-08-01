package mcp

import (
	"context"
	"net/http"
	"xprem/internal/services"
	"xprem/internal/version"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ConfigureServer populates one session's server for its principal; the tool
// packages provide these, the composition root passes them in.
type ConfigureServer func(ctx context.Context, principal *services.DashboardPrincipal, server *mcpprot.Server)

type MCPService struct {
	streamable *mcpprot.StreamableHTTPHandler
}

func NewMCPService(configurators ...ConfigureServer) *MCPService {
	// The tool schemas are inferred by reflection and never change, so every
	// session server shares one cache.
	schemas := &mcpprot.SchemaCache{}
	// One server per request: the tool list is per account, so what a
	// request's tools/list shows is already filtered to what its principal
	// may use.
	newSessionServer := func(req *http.Request) *mcpprot.Server {
		server := mcpprot.NewServer(&mcpprot.Implementation{
			Name:    "xprem",
			Version: version.Version,
			Title:   "xprem",
		}, &mcpprot.ServerOptions{SchemaCache: schemas})
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
			// Sessions live in process memory; behind a load balancer with
			// several replicas, a session created on one replica 404s on the
			// others. Stateless makes every POST self-contained.
			Stateless: true,
		},
	)
	return &MCPService{streamable: streamable}
}
