package mcptools

import (
	"context"
	"errors"
	"log"
	"time"
	"xprem/internal/repository"
	"xprem/internal/services"
	"xprem/internal/types"
	"xprem/internal/validation"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Keep in sync with the route twins in routes_app.go.
var (
	buildReadAccess     = Access{Perm: "build:read", Fallback: FallbackAnyMember}
	buildDownloadAccess = Access{Perm: "build:download", Fallback: FallbackAnyMember}
)

const (
	defaultBuildsLimit      = 20
	maxBuildsLimit          = 50
	maxBuildLogBytesPerCall = 64 << 10
	buildDownloadURLTTL     = time.Minute
)

type BuildReader interface {
	List(ctx context.Context, appID string, limit, offset int32, rawCursor string) (types.BuildsPage, error)
	Get(ctx context.Context, appID, id string) (*types.BuildRecord, error)
	ListLogs(ctx context.Context, appID, id string, after int32) ([]types.BuildLogChunk, error)
	DownloadURL(ctx context.Context, record types.BuildRecord, shareExpiresAt time.Time) (string, error)
}

func buildError(err error, action, appID string) error {
	var notFound *repository.ErrResourceNotFound
	switch {
	case errors.As(err, &notFound):
		return errors.New("build not found; list the builds with get_builds")
	case validation.IsValidationError(err):
		return err
	case errors.Is(err, repository.ErrNotSupportedInStatelessMode):
		return errors.New("builds require the control plane; this deployment runs in stateless mode")
	case errors.Is(err, services.ErrBuildNotReady):
		return errors.New("only a ready build can be downloaded")
	case errors.Is(err, services.ErrBuildStorageUnavailable):
		return err
	}
	log.Printf("mcp could not %s for app %s: %v", action, appID, err)
	return errors.New("could not " + action + ", try again later")
}

type GetBuildsInput struct {
	AppId  string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum builds returned; default 20, max 50"`
	Cursor string `json:"cursor,omitempty" jsonschema:"page forward: pass the nextCursor of a previous answer to fetch older builds"`
}

func getBuildsHandler(deps Deps) func(context.Context, *mcpprot.CallToolRequest, GetBuildsInput) (*mcpprot.CallToolResult, types.BuildsPage, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetBuildsInput) (*mcpprot.CallToolResult, types.BuildsPage, error) {
		ctx, _, err := requireAppPermission(ctx, deps, req, input.AppId, buildReadAccess)
		if err != nil {
			return nil, types.BuildsPage{}, err
		}
		limit := input.Limit
		if limit <= 0 {
			limit = defaultBuildsLimit
		}
		limit = min(limit, maxBuildsLimit)
		page, err := deps.Builds.List(ctx, input.AppId, int32(limit), 0, input.Cursor)
		if err != nil {
			return nil, types.BuildsPage{}, buildError(err, "list the builds", input.AppId)
		}
		if page.Builds == nil {
			page.Builds = []types.BuildRecord{}
		}
		return nil, page, nil
	}
}

type GetBuildInput struct {
	AppId   string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	BuildId string `json:"buildId" jsonschema:"the build id, as returned by get_builds"`
}

func getBuildHandler(deps Deps) func(context.Context, *mcpprot.CallToolRequest, GetBuildInput) (*mcpprot.CallToolResult, types.BuildRecord, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetBuildInput) (*mcpprot.CallToolResult, types.BuildRecord, error) {
		ctx, _, err := requireAppPermission(ctx, deps, req, input.AppId, buildReadAccess)
		if err != nil {
			return nil, types.BuildRecord{}, err
		}
		build, err := deps.Builds.Get(ctx, input.AppId, input.BuildId)
		if err != nil {
			return nil, types.BuildRecord{}, buildError(err, "read the build", input.AppId)
		}
		return nil, *build, nil
	}
}

type GetBuildLogsInput struct {
	AppId   string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	BuildId string `json:"buildId" jsonschema:"the build id, as returned by get_builds"`
	After   int32  `json:"after,omitempty" jsonschema:"byte offset to read from; pass the nextOffset of a previous answer to continue"`
}

type GetBuildLogsOutput struct {
	Chunks     []types.BuildLogChunk `json:"chunks"`
	NextOffset int32                 `json:"nextOffset" jsonschema:"pass it back as after to read the rest of the log"`
	HasMore    bool                  `json:"hasMore" jsonschema:"true when more log was already stored past nextOffset; a running build may still append after that"`
}

func getBuildLogsHandler(deps Deps) func(context.Context, *mcpprot.CallToolRequest, GetBuildLogsInput) (*mcpprot.CallToolResult, GetBuildLogsOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetBuildLogsInput) (*mcpprot.CallToolResult, GetBuildLogsOutput, error) {
		ctx, _, err := requireAppPermission(ctx, deps, req, input.AppId, buildReadAccess)
		if err != nil {
			return nil, GetBuildLogsOutput{}, err
		}
		chunks, err := deps.Builds.ListLogs(ctx, input.AppId, input.BuildId, input.After)
		if err != nil {
			return nil, GetBuildLogsOutput{}, buildError(err, "read the build log", input.AppId)
		}
		output := GetBuildLogsOutput{Chunks: []types.BuildLogChunk{}, NextOffset: input.After}
		size := 0
		for i, chunk := range chunks {
			if i > 0 && size+len(chunk.Content) > maxBuildLogBytesPerCall {
				output.HasMore = true
				break
			}
			size += len(chunk.Content)
			output.Chunks = append(output.Chunks, chunk)
			output.NextOffset = chunk.Offset + int32(len(chunk.Content))
		}
		return nil, output, nil
	}
}

type GetBuildDownloadURLOutput struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func getBuildDownloadURLHandler(deps Deps) func(context.Context, *mcpprot.CallToolRequest, GetBuildInput) (*mcpprot.CallToolResult, GetBuildDownloadURLOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetBuildInput) (*mcpprot.CallToolResult, GetBuildDownloadURLOutput, error) {
		ctx, _, err := requireAppPermission(ctx, deps, req, input.AppId, buildDownloadAccess)
		if err != nil {
			return nil, GetBuildDownloadURLOutput{}, err
		}
		build, err := deps.Builds.Get(ctx, input.AppId, input.BuildId)
		if err != nil {
			return nil, GetBuildDownloadURLOutput{}, buildError(err, "read the build", input.AppId)
		}
		expiresAt := time.Now().Add(buildDownloadURLTTL)
		url, err := deps.Builds.DownloadURL(ctx, *build, expiresAt)
		if err != nil {
			return nil, GetBuildDownloadURLOutput{}, buildError(err, "sign the download link", input.AppId)
		}
		if url == "" {
			return nil, GetBuildDownloadURLOutput{}, errors.New("this server's storage cannot sign download links; download the build from the dashboard")
		}
		return nil, GetBuildDownloadURLOutput{URL: url, ExpiresAt: expiresAt}, nil
	}
}

func registerGetBuilds(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_builds",
		Description: "The native builds of an app (appId required), newest first, max 50 per call, with status, artifact type, version and git metadata. When nextCursor is present, pass it back as cursor for the next page. Requires the build:read permission on the app.",
		Annotations: &mcpprot.ToolAnnotations{Title: "List builds", ReadOnlyHint: true},
	}, getBuildsHandler(deps))
}

func registerGetBuild(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_build",
		Description: "One native build of an app (appId and buildId required): status, artifact, version, git metadata and timing. Requires the build:read permission on the app.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Build details", ReadOnlyHint: true},
	}, getBuildHandler(deps))
}

func registerGetBuildLogs(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_build_logs",
		Description: "The log of a native build (appId and buildId required), about 64 KiB per call. Chunks contain NDJSON with one JSON event per line. While hasMore is true, pass nextOffset back as after. Requires the build:read permission on the app.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Build log", ReadOnlyHint: true},
	}, getBuildLogsHandler(deps))
}

func registerGetBuildDownloadURL(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_build_download_url",
		Description: "A signed link to download the artifact (APK, AAB or IPA) of a ready build (appId and buildId required). The link expires after one minute: fetch it right away. Requires the build:download permission on the app.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Build download link", ReadOnlyHint: true},
	}, getBuildDownloadURLHandler(deps))
}
