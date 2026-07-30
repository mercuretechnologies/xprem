package mcptools

import (
	"context"
	"errors"
	"log"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

type AppOutput struct {
	Id   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type GetAppsOutput struct {
	Apps []AppOutput `json:"apps"`
}

func getAppsHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, _ struct{}) (*mcpprot.CallToolResult, GetAppsOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, _ struct{}) (*mcpprot.CallToolResult, GetAppsOutput, error) {
		principal := principalFromRequest(req)
		if principal == nil {
			return nil, GetAppsOutput{}, errors.New("no authenticated account on this session")
		}
		apps, err := deps.Apps.GetApps(ctx)
		if err != nil {
			log.Printf("mcp get_apps could not list apps: %v", err)
			return nil, GetAppsOutput{}, errors.New("could not list the apps, try again later")
		}
		restricted, visible, err := deps.VisibleApps(ctx, principal)
		if err != nil {
			log.Printf("mcp get_apps could not resolve the visible apps of user %s: %v", principal.UserId, err)
			return nil, GetAppsOutput{}, errors.New("could not list the apps, try again later")
		}
		output := GetAppsOutput{Apps: []AppOutput{}}
		for _, app := range apps {
			if restricted && !visible[app.Id] {
				continue
			}
			output.Apps = append(output.Apps, AppOutput{Id: app.Id, Name: app.Name})
		}
		return nil, output, nil
	}
}

func registerGetApps(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_apps",
		Description: "The apps this account can see, with their id and name. Apps not listed are not visible to this account. Use the id as the appId argument of the other tools.",
	}, getAppsHandler(deps))
}
