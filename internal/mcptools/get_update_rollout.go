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
	AppId string `json:"appId"`
	// Branch (name) or BranchId designates the branch; one of the two is
	// required.
	Branch         string `json:"branch,omitempty"`
	BranchId       string `json:"branchId,omitempty"`
	RuntimeVersion string `json:"runtimeVersion"`
}

type GetUpdateRolloutOutput struct {
	Active  bool                  `json:"active"`
	Updates []types.RolloutUpdate `json:"updates"`
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
		if (input.Branch == "" && input.BranchId == "") || input.RuntimeVersion == "" {
			return nil, GetUpdateRolloutOutput{}, errors.New("branch (or branchId) and runtimeVersion are required; find them with get_branches and get_runtime_versions")
		}
		branchName, err := resolveBranchName(ctx, deps, input.AppId, input.Branch, input.BranchId)
		if err != nil {
			return nil, GetUpdateRolloutOutput{}, err
		}
		updates, err := deps.UpdateRollouts.GetUpdateRollout(ctx, input.AppId, branchName, input.RuntimeVersion)
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
