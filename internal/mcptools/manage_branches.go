package mcptools

import (
	"context"
	"errors"
	"log"
	"strconv"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	branchCreateAccess = Access{Perm: "branch:create", Fallback: FallbackAdminOnly}
	branchDeleteAccess = Access{Perm: "branch:delete", Fallback: FallbackAdminOnly}
)

type CreateBranchInput struct {
	AppId  string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	Branch string `json:"branch" jsonschema:"the branch name to create"`
}

type CreateBranchOutput struct {
	BranchId string `json:"branchId"`
	Branch   string `json:"branch"`
}

func createBranchHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input CreateBranchInput) (*mcpprot.CallToolResult, CreateBranchOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input CreateBranchInput) (*mcpprot.CallToolResult, CreateBranchOutput, error) {
		ctx, principal, err := requireAppPermission(ctx, deps, req, input.AppId, branchCreateAccess)
		if err != nil {
			return nil, CreateBranchOutput{}, err
		}
		if input.Branch == "" {
			return nil, CreateBranchOutput{}, errors.New("branch is required")
		}
		branchId, err := deps.BranchWriter.CreateBranch(ctx, input.AppId, input.Branch)
		if err != nil {
			return nil, CreateBranchOutput{}, writeError(err, "create the branch", "mcp create_branch", principal, input.AppId)
		}
		// Ids travel as strings: an int64 as a JSON number corrupts past 2^53.
		return nil, CreateBranchOutput{BranchId: strconv.FormatInt(branchId, 10), Branch: input.Branch}, nil
	}
}

func registerCreateBranch(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "create_branch",
		Description: "Create a branch on an app (appId and branch required). Requires the branch:create permission.",
		Annotations: &mcpprot.ToolAnnotations{
			Title:           "Create branch",
			DestructiveHint: boolPtr(false),
			IdempotentHint:  false,
		},
	}, createBranchHandler(deps))
}

type DeleteBranchInput struct {
	AppId  string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	Branch string `json:"branch" jsonschema:"the branch name to delete, as returned by get_branches"`
}

type DeleteBranchOutput struct {
	Deleted bool   `json:"deleted"`
	Branch  string `json:"branch"`
}

func deleteBranchHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input DeleteBranchInput) (*mcpprot.CallToolResult, DeleteBranchOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input DeleteBranchInput) (*mcpprot.CallToolResult, DeleteBranchOutput, error) {
		ctx, principal, err := requireAppPermission(ctx, deps, req, input.AppId, branchDeleteAccess)
		if err != nil {
			return nil, DeleteBranchOutput{}, err
		}
		if input.Branch == "" {
			return nil, DeleteBranchOutput{}, errors.New("branch is required; list the branches with get_branches")
		}
		// Protection, active channels and active rollouts are all refused by
		// the service and the store; their messages name what to unblock.
		if err := deps.BranchWriter.DeleteBranch(ctx, input.Branch, input.AppId); err != nil {
			return nil, DeleteBranchOutput{}, writeError(err, "delete the branch", "mcp delete_branch", principal, input.AppId)
		}
		log.Printf("mcp delete_branch: user %s deleted branch %s of app %s", principal.UserId, input.Branch, input.AppId)
		return nil, DeleteBranchOutput{Deleted: true, Branch: input.Branch}, nil
	}
}

func registerDeleteBranch(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "delete_branch",
		Description: "Delete a branch of an app (appId and branch required), along with the update files it holds. Refused when the branch is protected, mapped to a channel, or serving an active rollout. Requires the branch:delete permission.",
		Annotations: &mcpprot.ToolAnnotations{
			Title:           "Delete branch",
			DestructiveHint: boolPtr(true),
			IdempotentHint:  false,
		},
	}, deleteBranchHandler(deps))
}
