package mcptools

import (
	"context"
	"errors"
	"expo-open-ota/internal/store"
	"log"
	"time"

	"expo-open-ota/internal/types"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultUpdatesLimit = 20
	maxUpdatesLimit     = 50
)

type GetUpdatesInput struct {
	AppId string `json:"appId"`
	// Every filter narrows the feed; combine them to pinpoint an update.
	// Branch is the branch name, as returned by get_branches.
	Branch         string `json:"branch,omitempty"`
	RuntimeVersion string `json:"runtimeVersion,omitempty"`
	Platform       string `json:"platform,omitempty"`
	UpdateUUID     string `json:"updateUUID,omitempty"`
	PublishGroup   string `json:"publishGroup,omitempty"`
	CommitHash     string `json:"commitHash,omitempty"`
	// From and To bound the publication date, RFC3339.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Limit caps the answer; default 20, max 50. Use the filters rather than
	// a big limit.
	Limit int `json:"limit,omitempty"`
}

type GetUpdatesOutput struct {
	Updates []types.UpdateFeedItem `json:"updates"`
}

func getUpdatesHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetUpdatesInput) (*mcpprot.CallToolResult, GetUpdatesOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetUpdatesInput) (*mcpprot.CallToolResult, GetUpdatesOutput, error) {
		principal := PrincipalFromRequest(req)
		if principal == nil {
			return nil, GetUpdatesOutput{}, errors.New("no authenticated account on this session")
		}
		if err := requireAppVisible(ctx, deps, principal, input.AppId); err != nil {
			return nil, GetUpdatesOutput{}, err
		}

		limit := input.Limit
		if limit <= 0 {
			limit = defaultUpdatesLimit
		}
		if limit > maxUpdatesLimit {
			limit = maxUpdatesLimit
		}
		query := types.UpdateFeedQuery{
			Branch:         input.Branch,
			RuntimeVersion: input.RuntimeVersion,
			Platform:       input.Platform,
			UpdateUUID:     input.UpdateUUID,
			PublishGroup:   input.PublishGroup,
			CommitHash:     input.CommitHash,
			Limit:          limit,
		}
		for _, bound := range []struct {
			raw    string
			target **time.Time
		}{{input.From, &query.From}, {input.To, &query.To}} {
			if bound.raw == "" {
				continue
			}
			parsed, err := time.Parse(time.RFC3339, bound.raw)
			if err != nil {
				return nil, GetUpdatesOutput{}, errors.New("from and to must be RFC3339 timestamps")
			}
			*bound.target = &parsed
		}

		updates, err := deps.UpdateFeed.GetUpdateFeed(ctx, input.AppId, query)
		if err != nil {
			if errors.Is(err, store.ErrNotSupportedInStatelessMode) {
				return nil, GetUpdatesOutput{}, errors.New("the update feed requires the control plane; this deployment runs in stateless mode")
			}
			log.Printf("mcp get_updates failed for app %s: %v", input.AppId, err)
			return nil, GetUpdatesOutput{}, errors.New("could not list the updates, try again later")
		}
		if updates == nil {
			updates = []types.UpdateFeedItem{}
		}
		return nil, GetUpdatesOutput{Updates: updates}, nil
	}
}

func registerGetUpdates(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_updates",
		Description: "The published updates of an app (appId required), newest first, max 50 per call. Narrow with branch, runtimeVersion, platform, updateUUID, publishGroup, commitHash, or a from/to date range instead of paging.",
	}, getUpdatesHandler(deps))
}
