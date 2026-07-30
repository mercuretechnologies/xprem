package mcptools

import (
	"context"
	"errors"
	"log"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

type WhoamiOutput struct {
	UserId string `json:"userId"`
	Email  string `json:"email"`
	// Permissions is the account's full authorization picture: what it holds
	// and what it lacks, so a denied action never needs guessing.
	Permissions AccountPermissions `json:"permissions"`
}

func whoamiHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, _ struct{}) (*mcpprot.CallToolResult, WhoamiOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, _ struct{}) (*mcpprot.CallToolResult, WhoamiOutput, error) {
		principal := principalFromRequest(req)
		if principal == nil {
			return nil, WhoamiOutput{}, errors.New("no authenticated account on this session")
		}
		apps, err := deps.Apps.GetApps(ctx)
		if err != nil {
			log.Printf("mcp whoami could not list apps: %v", err)
			return nil, WhoamiOutput{}, errors.New("could not resolve the account's permissions, try again later")
		}
		appIDs := make([]string, len(apps))
		for i, app := range apps {
			appIDs[i] = app.Id
		}
		permissions, err := deps.DescribePermissions(ctx, principal, appIDs)
		if err != nil {
			return nil, WhoamiOutput{}, errors.New("could not resolve the account's permissions, try again later")
		}
		return nil, WhoamiOutput{
			UserId:      principal.UserId,
			Email:       principal.Email,
			Permissions: permissions,
		}, nil
	}
}

func registerWhoami(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "whoami",
		Description: "The account this MCP connection acts as, with its full permission picture: role, and for each app the account can see, every permission granted or denied. Apps not listed are not visible to this account. Every other tool runs with these permissions.",
	}, whoamiHandler(deps))
}
