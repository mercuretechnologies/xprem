package mcptools

import (
	"context"
	"errors"
	"log"
	"time"
	"xprem/internal/store"

	"xprem/internal/types"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultUpdatesLimit = 20
	maxUpdatesLimit     = 50
)

type GetUpdatesInput struct {
	AppId          string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	Branch         string `json:"branch,omitempty" jsonschema:"filter by branch name, as returned by get_branches"`
	RuntimeVersion string `json:"runtimeVersion,omitempty" jsonschema:"filter by runtime version, as returned by get_runtime_versions"`
	Platform       string `json:"platform,omitempty" jsonschema:"filter by platform: ios or android"`
	UpdateUUID     string `json:"updateUUID,omitempty" jsonschema:"filter by updateUUID; matches any update whose uuid contains this value, case-insensitive"`
	PublishGroup   string `json:"publishGroup,omitempty" jsonschema:"filter by publish group id (the updates published together); matches by substring, case-insensitive"`
	CommitHash     string `json:"commitHash,omitempty" jsonschema:"filter by the git commit hash the update was built from; matches by substring, case-insensitive"`
	From           string `json:"from,omitempty" jsonschema:"only updates published at or after this RFC3339 timestamp"`
	To             string `json:"to,omitempty" jsonschema:"only updates published at or before this RFC3339 timestamp"`
	Limit          int    `json:"limit,omitempty" jsonschema:"maximum updates returned; default 20, max 50; prefer narrowing with filters over raising it"`
	Cursor         string `json:"cursor,omitempty" jsonschema:"page forward: pass the nextCursor of a previous answer to fetch older updates"`
}

type GetUpdatesOutput struct {
	Updates []types.UpdateFeedItem `json:"updates"`
	// NextCursor is set when more updates exist past this page.
	NextCursor string `json:"nextCursor,omitempty" jsonschema:"present when more updates exist; pass it back as cursor to fetch the next page"`
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

		var platform types.Platform
		if input.Platform != "" {
			parsed, err := types.ParsePlatform(input.Platform)
			if err != nil {
				return nil, GetUpdatesOutput{}, errors.New("platform must be ios or android")
			}
			platform = parsed
		}
		limit := input.Limit
		if limit <= 0 {
			limit = defaultUpdatesLimit
		}
		if limit > maxUpdatesLimit {
			limit = maxUpdatesLimit
		}
		// One extra row answers "is there a next page"; it is never returned.
		query := types.UpdateFeedQuery{
			Branch:         input.Branch,
			RuntimeVersion: input.RuntimeVersion,
			Platform:       platform,
			UpdateUUID:     input.UpdateUUID,
			PublishGroup:   input.PublishGroup,
			CommitHash:     input.CommitHash,
			Limit:          limit + 1,
		}
		cursor, err := types.DecodeUpdateFeedCursor(input.Cursor)
		if err != nil {
			return nil, GetUpdatesOutput{}, errors.New("cursor is invalid; pass a nextCursor from a previous answer")
		}
		if cursor != nil {
			query.CursorCreatedAt = &cursor.CreatedAt
			query.CursorBranchID = cursor.BranchID
			query.CursorUpdateID = cursor.UpdateID
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
		output := GetUpdatesOutput{Updates: updates}
		if len(updates) > limit {
			output.Updates = updates[:limit]
			cursor, err := types.EncodeUpdateFeedCursor(output.Updates[len(output.Updates)-1])
			if err != nil {
				// The page is valid; only paging past it is not. Answer it
				// without a cursor rather than failing the call.
				log.Printf("mcp get_updates could not encode the cursor for app %s: %v", input.AppId, err)
			}
			output.NextCursor = cursor
		}
		return nil, output, nil
	}
}

func registerGetUpdates(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_updates",
		Description: "The published updates of an app (appId required), newest first, max 50 per call. Narrow with branch, runtimeVersion, platform, updateUUID, publishGroup, commitHash, or a from/to date range; when nextCursor is present, pass it back as cursor for the next page.",
		Annotations: &mcpprot.ToolAnnotations{Title: "List updates", ReadOnlyHint: true},
	}, getUpdatesHandler(deps))
}
