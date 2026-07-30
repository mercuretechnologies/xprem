package mcptools

import (
	"context"
	"errors"
	"log"
	"strings"

	"expo-open-ota/internal/types"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

type GetBranchesInput struct {
	AppId string `json:"appId"`
	// Name and Id each narrow the answer to one branch; exact match.
	Name string `json:"name,omitempty"`
	Id   string `json:"id,omitempty"`
}

// matchesBranch applies the optional name/id narrowing.
func matchesBranch(branch types.BranchMapping, name string, id string) bool {
	if name != "" && !strings.EqualFold(branch.BranchName, name) {
		return false
	}
	if id != "" && (branch.BranchId == nil || *branch.BranchId != id) {
		return false
	}
	return true
}

type GetBranchesOutput struct {
	Branches []types.BranchMapping `json:"branches"`
}

func getBranchesHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetBranchesInput) (*mcpprot.CallToolResult, GetBranchesOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetBranchesInput) (*mcpprot.CallToolResult, GetBranchesOutput, error) {
		principal := PrincipalFromRequest(req)
		if principal == nil {
			return nil, GetBranchesOutput{}, errors.New("no authenticated account on this session")
		}
		if err := requireAppVisible(ctx, deps, principal, input.AppId); err != nil {
			return nil, GetBranchesOutput{}, err
		}
		branches, err := deps.Branches.GetBranches(ctx, input.AppId)
		if err != nil {
			log.Printf("mcp get_branches failed for app %s: %v", input.AppId, err)
			return nil, GetBranchesOutput{}, errors.New("could not list the branches, try again later")
		}
		output := GetBranchesOutput{Branches: []types.BranchMapping{}}
		for _, branch := range branches {
			if !matchesBranch(branch, input.Name, input.Id) {
				continue
			}
			output.Branches = append(output.Branches, branch)
		}
		return nil, output, nil
	}
}

func registerGetBranches(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_branches",
		Description: "The branches of an app (appId required, from get_apps), with their release channel, protection flag and current update. Pass name or id to fetch a single branch.",
	}, getBranchesHandler(deps))
}

type GetRuntimeVersionsInput struct {
	AppId string `json:"appId"`
	// Branch is the branch name, as returned by get_branches.
	Branch string `json:"branch"`
}

type GetRuntimeVersionsOutput struct {
	RuntimeVersions []types.RuntimeVersionWithStats `json:"runtimeVersions"`
}

func getRuntimeVersionsHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetRuntimeVersionsInput) (*mcpprot.CallToolResult, GetRuntimeVersionsOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetRuntimeVersionsInput) (*mcpprot.CallToolResult, GetRuntimeVersionsOutput, error) {
		principal := PrincipalFromRequest(req)
		if principal == nil {
			return nil, GetRuntimeVersionsOutput{}, errors.New("no authenticated account on this session")
		}
		if err := requireAppVisible(ctx, deps, principal, input.AppId); err != nil {
			return nil, GetRuntimeVersionsOutput{}, err
		}
		if input.Branch == "" {
			return nil, GetRuntimeVersionsOutput{}, errors.New("branch is required; list the branches with get_branches")
		}
		versions, err := deps.Branches.GetRuntimeVersionsWithUpdateStats(ctx, input.AppId, input.Branch)
		if err != nil {
			log.Printf("mcp get_runtime_versions failed for app %s branch %s: %v", input.AppId, input.Branch, err)
			return nil, GetRuntimeVersionsOutput{}, errors.New("could not list the runtime versions, try again later")
		}
		return nil, GetRuntimeVersionsOutput{RuntimeVersions: versions}, nil
	}
}

func registerGetRuntimeVersions(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_runtime_versions",
		Description: "The runtime versions published on a branch (appId and branch name required, from get_branches), with update counts, last publication date and active rollout state.",
	}, getRuntimeVersionsHandler(deps))
}
