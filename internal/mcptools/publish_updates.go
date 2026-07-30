package mcptools

import (
	"context"
	"errors"
	"log"
	"strings"

	"expo-open-ota/internal/types"

	"github.com/google/uuid"
	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// publishAccess gates both write tools below, like the route twin: rolling
// back and republishing are the same permission.
var publishAccess = Access{Perm: "update:publish", Fallback: FallbackAdminOnly}

type RollbackInput struct {
	AppId          string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	Branch         string `json:"branch" jsonschema:"the branch name to roll back, as returned by get_branches"`
	RuntimeVersion string `json:"runtimeVersion" jsonschema:"the runtime version to roll back, as returned by get_runtime_versions"`
	Message        string `json:"message" jsonschema:"why this rollback happens; recorded on the update and in the audit log"`
	Platform       string `json:"platform,omitempty" jsonschema:"limit the rollback to ios or android; both platforms when omitted"`
}

type PublishOutput struct {
	Updates      []types.Update `json:"updates" jsonschema:"the updates this action created, one per platform it acted on"`
	PublishGroup string         `json:"publishGroup,omitempty" jsonschema:"the new publish group, set only when republishing a group"`
}

func rollbackHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input RollbackInput) (*mcpprot.CallToolResult, PublishOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input RollbackInput) (*mcpprot.CallToolResult, PublishOutput, error) {
		ctx, principal, err := requireAppPermission(ctx, deps, req, input.AppId, publishAccess)
		if err != nil {
			return nil, PublishOutput{}, err
		}
		if input.Branch == "" || input.RuntimeVersion == "" {
			return nil, PublishOutput{}, errors.New("branch and runtimeVersion are required; find them with get_branches and get_runtime_versions")
		}
		message := strings.TrimSpace(input.Message)
		if message == "" {
			return nil, PublishOutput{}, errors.New("message is required: say why this rollback happens")
		}
		platforms := []string{"ios", "android"}
		if input.Platform != "" {
			if input.Platform != "ios" && input.Platform != "android" {
				return nil, PublishOutput{}, errors.New("platform must be ios or android")
			}
			platforms = []string{input.Platform}
		}

		output := PublishOutput{Updates: []types.Update{}}
		for _, platform := range platforms {
			update, err := deps.Deployments.CreateRollback(ctx, input.AppId, platform, "", input.RuntimeVersion, input.Branch, message)
			if err != nil {
				// A partial rollback must be reported, not hidden: the caller
				// needs to know which platform is now on the embedded bundle.
				if len(output.Updates) > 0 {
					return nil, output, errors.New("rolled back " + output.Updates[0].RuntimeVersion + " for the first platform but failed for " + platform + ": " + err.Error())
				}
				return nil, PublishOutput{}, writeError(err, "roll back the branch", "mcp rollback_branch", principal, input.AppId)
			}
			output.Updates = append(output.Updates, *update)
		}
		log.Printf("mcp rollback_branch: user %s rolled back %s/%s of app %s", principal.UserId, input.Branch, input.RuntimeVersion, input.AppId)
		return nil, output, nil
	}
}

func registerRollback(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "rollback_branch",
		Description: "Roll a branch and runtime version back to the bundle embedded in the app binary (appId, branch, runtimeVersion and message required). Devices stop receiving the OTA update on their next check. Refused while a progressive rollout is active. Requires the update:publish permission.",
		Annotations: &mcpprot.ToolAnnotations{
			Title:           "Roll back to the embedded bundle",
			DestructiveHint: boolPtr(true),
			IdempotentHint:  false,
		},
	}, rollbackHandler(deps))
}

type RepublishInput struct {
	AppId          string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	Branch         string `json:"branch" jsonschema:"the branch name, as returned by get_branches"`
	RuntimeVersion string `json:"runtimeVersion" jsonschema:"the runtime version, as returned by get_runtime_versions"`
	UpdateId       string `json:"updateId,omitempty" jsonschema:"republish this single update; provide either updateId or publishGroup, not both"`
	PublishGroup   string `json:"publishGroup,omitempty" jsonschema:"republish every platform of this publish group (a UUID from get_updates); provide either updateId or publishGroup, not both"`
}

func republishHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input RepublishInput) (*mcpprot.CallToolResult, PublishOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input RepublishInput) (*mcpprot.CallToolResult, PublishOutput, error) {
		ctx, principal, err := requireAppPermission(ctx, deps, req, input.AppId, publishAccess)
		if err != nil {
			return nil, PublishOutput{}, err
		}
		if input.Branch == "" || input.RuntimeVersion == "" {
			return nil, PublishOutput{}, errors.New("branch and runtimeVersion are required; find them with get_branches and get_runtime_versions")
		}
		if (input.UpdateId == "") == (input.PublishGroup == "") {
			return nil, PublishOutput{}, errors.New("provide either updateId or publishGroup, not both")
		}

		if input.PublishGroup != "" {
			if _, err := uuid.Parse(input.PublishGroup); err != nil {
				return nil, PublishOutput{}, errors.New("publishGroup must be a UUID, as returned by get_updates")
			}
			result, err := deps.Deployments.RepublishPublishGroup(ctx, input.AppId, input.Branch, input.RuntimeVersion, input.PublishGroup)
			if err != nil {
				return nil, PublishOutput{}, writeError(err, "republish the publish group", "mcp republish_update", principal, input.AppId)
			}
			log.Printf("mcp republish_update: user %s republished group %s of app %s", principal.UserId, input.PublishGroup, input.AppId)
			return nil, PublishOutput{Updates: result.Updates, PublishGroup: result.PublishGroup}, nil
		}

		update, err := deps.Deployments.RepublishUpdateByID(ctx, input.AppId, input.Branch, input.RuntimeVersion, input.UpdateId)
		if err != nil {
			return nil, PublishOutput{}, writeError(err, "republish the update", "mcp republish_update", principal, input.AppId)
		}
		log.Printf("mcp republish_update: user %s republished update %s of app %s", principal.UserId, input.UpdateId, input.AppId)
		return nil, PublishOutput{Updates: []types.Update{*update}}, nil
	}
}

func registerRepublish(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "republish_update",
		Description: "Republish a past update, or every platform of a publish group, as the newest update of its branch and runtime version (appId, branch, runtimeVersion, and either updateId or publishGroup). Devices receive it on their next check. Refused while a progressive rollout is active. Requires the update:publish permission.",
		Annotations: &mcpprot.ToolAnnotations{
			Title:           "Republish a past update",
			DestructiveHint: boolPtr(false),
			IdempotentHint:  false,
		},
	}, republishHandler(deps))
}
