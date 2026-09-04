package mcptools

import (
	"context"
	"errors"
	"log"
	"xprem/internal/services"
	"xprem/internal/types"
	"xprem/internal/validation"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

type GetUpdatePatchesInput struct {
	AppId    string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	Branch   string `json:"branch" jsonschema:"the branch name, as returned by get_branches"`
	UpdateId string `json:"updateId" jsonschema:"the update id, as returned by get_updates"`
}

type GetUpdatePatchesOutput struct {
	Patches []types.BundlePatch `json:"patches" jsonschema:"one row per earlier update a bsdiff patch toward this update was planned from: its status (pending, running, stored, skipped, failed, cancelled), the reason when no patch was stored, and the patch size against the full download size"`
}

func getUpdatePatchesHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetUpdatePatchesInput) (*mcpprot.CallToolResult, GetUpdatePatchesOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetUpdatePatchesInput) (*mcpprot.CallToolResult, GetUpdatePatchesOutput, error) {
		principal := PrincipalFromRequest(req)
		if principal == nil {
			return nil, GetUpdatePatchesOutput{}, errors.New("no authenticated account on this session")
		}
		if err := requireAppVisible(ctx, deps, principal, input.AppId); err != nil {
			return nil, GetUpdatePatchesOutput{}, err
		}
		if input.Branch == "" || input.UpdateId == "" {
			return nil, GetUpdatePatchesOutput{}, errors.New("branch and updateId are required; find them with get_branches and get_updates")
		}
		patches, err := deps.BundlePatches.ListPatches(ctx, input.AppId, input.Branch, input.UpdateId)
		if err != nil {
			var valErr *validation.Error
			switch {
			case errors.Is(err, services.ErrBundleDiffingUnavailable):
				return nil, GetUpdatePatchesOutput{}, errors.New("bundle diffing is disabled on this server (BUNDLE_DIFFING) or needs the control plane")
			case errors.As(err, &valErr):
				return nil, GetUpdatePatchesOutput{}, valErr
			}
			log.Printf("mcp get_update_patches failed for app %s update %s: %v", input.AppId, input.UpdateId, err)
			return nil, GetUpdatePatchesOutput{}, errors.New("could not read the bundle patches, try again later")
		}
		return nil, GetUpdatePatchesOutput{Patches: patches}, nil
	}
}

func registerGetUpdatePatches(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_update_patches",
		Description: "The bsdiff bundle patches planned toward one update (appId, branch and updateId required): per earlier update of the same platform, whether a patch was stored, skipped, failed or is still computing, and how much smaller than the full bundle download it is. Only when bundle diffing is enabled on the server.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Update bundle patches", ReadOnlyHint: true},
	}, getUpdatePatchesHandler(deps))
}
