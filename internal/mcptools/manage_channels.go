package mcptools

import (
	"context"
	"errors"
	"log"
	"strconv"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	channelCreateAccess = Access{Perm: "channel:create", Fallback: FallbackAdminOnly}
	channelDeleteAccess = Access{Perm: "channel:delete", Fallback: FallbackAdminOnly}
)

type CreateChannelInput struct {
	AppId   string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	Channel string `json:"channel" jsonschema:"the release channel name to create"`
	Branch  string `json:"branch,omitempty" jsonschema:"optional branch name to point the channel at, as returned by get_branches; the channel is created unmapped when omitted"`
}

type CreateChannelOutput struct {
	ChannelId string `json:"channelId"`
	Channel   string `json:"channel"`
	Branch    string `json:"branch,omitempty"`
}

func createChannelHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input CreateChannelInput) (*mcpprot.CallToolResult, CreateChannelOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input CreateChannelInput) (*mcpprot.CallToolResult, CreateChannelOutput, error) {
		ctx, principal, err := requireAppPermission(ctx, deps, req, input.AppId, channelCreateAccess)
		if err != nil {
			return nil, CreateChannelOutput{}, err
		}
		if input.Channel == "" {
			return nil, CreateChannelOutput{}, errors.New("channel is required")
		}
		var branchName *string
		if input.Branch != "" {
			branchName = &input.Branch
		}
		channelId, err := deps.ChannelWriter.CreateChannel(ctx, input.AppId, branchName, input.Channel)
		if err != nil {
			return nil, CreateChannelOutput{}, writeError(err, "create the channel", "mcp create_channel", principal, input.AppId)
		}
		// Ids travel as strings: an int64 as a JSON number corrupts past 2^53.
		return nil, CreateChannelOutput{
			ChannelId: strconv.FormatInt(channelId, 10),
			Channel:   input.Channel,
			Branch:    input.Branch,
		}, nil
	}
}

func registerCreateChannel(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "create_channel",
		Description: "Create a release channel on an app (appId and channel required), optionally pointing it at a branch. Requires the channel:create permission.",
		Annotations: &mcpprot.ToolAnnotations{
			Title:           "Create channel",
			DestructiveHint: boolPtr(false),
			IdempotentHint:  false,
		},
	}, createChannelHandler(deps))
}

type DeleteChannelInput struct {
	AppId   string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	Channel string `json:"channel" jsonschema:"the release channel name to delete, as returned by get_channels"`
}

type DeleteChannelOutput struct {
	Deleted bool   `json:"deleted"`
	Channel string `json:"channel"`
}

func deleteChannelHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input DeleteChannelInput) (*mcpprot.CallToolResult, DeleteChannelOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input DeleteChannelInput) (*mcpprot.CallToolResult, DeleteChannelOutput, error) {
		ctx, principal, err := requireAppPermission(ctx, deps, req, input.AppId, channelDeleteAccess)
		if err != nil {
			return nil, DeleteChannelOutput{}, err
		}
		if input.Channel == "" {
			return nil, DeleteChannelOutput{}, errors.New("channel is required; list the channels with get_channels")
		}
		if err := deps.ChannelWriter.DeleteChannel(ctx, input.Channel, input.AppId); err != nil {
			return nil, DeleteChannelOutput{}, writeError(err, "delete the channel", "mcp delete_channel", principal, input.AppId)
		}
		log.Printf("mcp delete_channel: user %s deleted channel %s of app %s", principal.UserId, input.Channel, input.AppId)
		return nil, DeleteChannelOutput{Deleted: true, Channel: input.Channel}, nil
	}
}

func registerDeleteChannel(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "delete_channel",
		Description: "Delete a release channel of an app (appId and channel required). Devices still pointing at this channel stop receiving updates. Requires the channel:delete permission.",
		Annotations: &mcpprot.ToolAnnotations{
			Title:           "Delete channel",
			DestructiveHint: boolPtr(true),
			IdempotentHint:  false,
		},
	}, deleteChannelHandler(deps))
}
