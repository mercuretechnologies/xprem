package mcptools

import (
	"context"
	"errors"
	"expo-open-ota/internal/services"
	"log"

	"expo-open-ota/internal/types"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

type GetUpdateRolloutInput struct {
	AppId          string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	Branch         string `json:"branch" jsonschema:"the branch name, as returned by get_branches"`
	RuntimeVersion string `json:"runtimeVersion" jsonschema:"the runtime version, as returned by get_runtime_versions"`
}

type GetUpdateRolloutOutput struct {
	Active  bool                  `json:"active" jsonschema:"false when no progressive rollout is in progress on this branch and runtime version"`
	Updates []types.RolloutUpdate `json:"updates" jsonschema:"the rolled-out percentage per platform for the latest update"`
}

func getUpdateRolloutHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetUpdateRolloutInput) (*mcpprot.CallToolResult, GetUpdateRolloutOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetUpdateRolloutInput) (*mcpprot.CallToolResult, GetUpdateRolloutOutput, error) {
		principal := PrincipalFromRequest(req)
		if principal == nil {
			return nil, GetUpdateRolloutOutput{}, errors.New("no authenticated account on this session")
		}
		if err := requireAppVisible(ctx, deps, principal, input.AppId); err != nil {
			return nil, GetUpdateRolloutOutput{}, err
		}
		if input.Branch == "" || input.RuntimeVersion == "" {
			return nil, GetUpdateRolloutOutput{}, errors.New("branch and runtimeVersion are required; find them with get_branches and get_runtime_versions")
		}
		updates, err := deps.UpdateRollouts.GetUpdateRollout(ctx, input.AppId, input.Branch, input.RuntimeVersion)
		if err != nil {
			if errors.Is(err, services.ErrRolloutsRequireControlPlane) {
				return nil, GetUpdateRolloutOutput{}, errors.New("rollouts require the control plane; this deployment runs in stateless mode")
			}
			log.Printf("mcp get_update_rollout failed for app %s branch %s: %v", input.AppId, input.Branch, err)
			return nil, GetUpdateRolloutOutput{}, errors.New("could not read the update rollout, try again later")
		}
		if updates == nil {
			updates = []types.RolloutUpdate{}
		}
		return nil, GetUpdateRolloutOutput{Active: len(updates) > 0, Updates: updates}, nil
	}
}

func registerGetUpdateRollout(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_update_rollout",
		Description: "The progressive rollout state of the latest update on one branch and runtime version (appId, branch and runtimeVersion required): the rolled-out percentage per platform, or active=false when no rollout is in progress.",
	}, getUpdateRolloutHandler(deps))
}
