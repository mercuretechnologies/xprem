package mcp

import (
	"expo-open-ota/internal/version"
	"net/http"

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
		nil,
	)
	return &MCPService{server: server, streamable: streamable}
}
