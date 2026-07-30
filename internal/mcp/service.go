package mcp

import (
	"expo-open-ota/internal/version"
	"net/http"
	"time"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

type MCPService struct {
	server     *mcpprot.Server
	streamable *mcpprot.StreamableHTTPHandler
}

func NewMCPService() *MCPService {
	server := mcpprot.NewServer(&mcpprot.Implementation{
		Name:    "Expo-Open-Ota",
		Version: version.Version,
		Title:   "Expo Open OTA",
	}, nil)

	streamable := mcpprot.NewStreamableHTTPHandler(
		func(*http.Request) *mcpprot.Server { return server },
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
	return &MCPService{server: server, streamable: streamable}
}
