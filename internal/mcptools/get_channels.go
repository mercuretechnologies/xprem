package mcptools

import (
	"context"
	"errors"
	"log"
	"strings"

	"expo-open-ota/internal/types"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

type GetChannelsInput struct {
	AppId string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	Name  string `json:"name,omitempty" jsonschema:"exact channel name, to fetch a single channel"`
	Id    string `json:"id,omitempty" jsonschema:"exact channel id, to fetch a single channel"`
}

// matchesChannel applies the optional name/id narrowing.
func matchesChannel(channel types.ChannelMapping, name string, id string) bool {
	if name != "" && !strings.EqualFold(channel.ReleaseChannelName, name) {
		return false
	}
	if id != "" && channel.ReleaseChannelId != id {
		return false
	}
	return true
}

type GetChannelsOutput struct {
	Channels []types.ChannelMapping `json:"channels"`
}

func getChannelsHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetChannelsInput) (*mcpprot.CallToolResult, GetChannelsOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetChannelsInput) (*mcpprot.CallToolResult, GetChannelsOutput, error) {
		principal := PrincipalFromRequest(req)
		if principal == nil {
			return nil, GetChannelsOutput{}, errors.New("no authenticated account on this session")
		}
		if err := requireAppVisible(ctx, deps, principal, input.AppId); err != nil {
			return nil, GetChannelsOutput{}, err
		}
		channels, err := deps.Channels.GetChannels(ctx, input.AppId)
		if err != nil {
			log.Printf("mcp get_channels failed for app %s: %v", input.AppId, err)
			return nil, GetChannelsOutput{}, errors.New("could not list the channels, try again later")
		}
		output := GetChannelsOutput{Channels: []types.ChannelMapping{}}
		for _, channel := range channels {
			if !matchesChannel(channel, input.Name, input.Id) {
				continue
			}
			output.Channels = append(output.Channels, channel)
		}
		return nil, output, nil
	}
}

func registerGetChannels(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_channels",
		Description: "The release channels of an app (appId required, from get_apps), with their linked branch, current updates, and the active progressive rollout if any. Pass name or id to fetch a single channel.",
	}, getChannelsHandler(deps))
}

type ChannelRolloutOutput struct {
	ChannelName string               `json:"channelName"`
	Rollout     types.ChannelRollout `json:"rollout"`
}

type GetChannelRolloutsOutput struct {
	Rollouts []ChannelRolloutOutput `json:"rollouts"`
}

func getChannelRolloutsHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetChannelsInput) (*mcpprot.CallToolResult, GetChannelRolloutsOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetChannelsInput) (*mcpprot.CallToolResult, GetChannelRolloutsOutput, error) {
		principal := PrincipalFromRequest(req)
		if principal == nil {
			return nil, GetChannelRolloutsOutput{}, errors.New("no authenticated account on this session")
		}
		if err := requireAppVisible(ctx, deps, principal, input.AppId); err != nil {
			return nil, GetChannelRolloutsOutput{}, err
		}
		channels, err := deps.Channels.GetChannels(ctx, input.AppId)
		if err != nil {
			log.Printf("mcp get_channel_rollouts failed for app %s: %v", input.AppId, err)
			return nil, GetChannelRolloutsOutput{}, errors.New("could not list the channel rollouts, try again later")
		}
		output := GetChannelRolloutsOutput{Rollouts: []ChannelRolloutOutput{}}
		for _, channel := range channels {
			if channel.Rollout == nil {
				continue
			}
			if !matchesChannel(channel, input.Name, input.Id) {
				continue
			}
			output.Rollouts = append(output.Rollouts, ChannelRolloutOutput{
				ChannelName: channel.ReleaseChannelName,
				Rollout:     *channel.Rollout,
			})
		}
		return nil, output, nil
	}
}

func registerGetChannelRollouts(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_channel_rollouts",
		Description: "The active progressive rollouts between branches on the channels of an app (appId required). An empty list means no channel rollout is in progress. Pass name or id to check a single channel.",
	}, getChannelRolloutsHandler(deps))
}
