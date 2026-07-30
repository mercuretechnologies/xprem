// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"xprem/ee/observe"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// The dashboard asks for 200 lines to fill a virtualized list; a reader
	// that pays per token wants a page it can actually read.
	defaultLogsPage = 20
	maxLogsPage     = 100
	// A crash body and its attributes can be arbitrarily long; past this the
	// line is cut and says so.
	maxLogFieldLength = 2000
)

type QueryLogsInput struct {
	AppId string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	ObserveFilters
	Severity   string   `json:"severity,omitempty" jsonschema:"keep only one band: debug, info, warn, error or fatal. These are EXCLUSIVE, not a threshold: warn returns warnings only, not errors. fatal also matches native launch crashes"`
	Search     string   `json:"search,omitempty" jsonschema:"case-insensitive text search over the event name, the body and the attributes"`
	EventNames []string `json:"eventNames,omitempty" jsonschema:"exact event names, as returned by get_observe_events"`
	From       string   `json:"from,omitempty" jsonschema:"start of the window, RFC3339; defaults to 24 hours before to, and cannot be more than 31 days before it"`
	To         string   `json:"to,omitempty" jsonschema:"end of the window, RFC3339; defaults to now"`
	Limit      int      `json:"limit,omitempty" jsonschema:"lines per page; default 20, max 100"`
	Cursor     string   `json:"cursor,omitempty" jsonschema:"page backwards in time: pass the nextCursor of a previous answer"`
}

// LogOutput is one line, trimmed to what reads usefully.
type LogOutput struct {
	Timestamp   string `json:"timestamp"`
	EventName   string `json:"eventName"`
	Severity    string `json:"severity,omitempty"`
	IsFatal     bool   `json:"isFatal,omitempty"`
	Body        string `json:"body,omitempty"`
	Attributes  string `json:"attributes,omitempty" jsonschema:"raw JSON of the attributes the SDK attached to the line"`
	EasClientId string `json:"easClientId,omitempty" jsonschema:"the install that emitted it; pass it to get_device"`
	SessionId   string `json:"sessionId,omitempty"`
	UpdateId    string `json:"updateId,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Channel     string `json:"channel,omitempty"`
	Platform    string `json:"platform,omitempty"`
	OsVersion   string `json:"osVersion,omitempty"`
	DeviceModel string `json:"deviceModel,omitempty"`
	AppVersion  string `json:"appVersion,omitempty"`
}

type QueryLogsOutput struct {
	Available bool        `json:"available" jsonschema:"false when this deployment has no ClickHouse: logs are not stored, so the list is empty"`
	Logs      []LogOutput `json:"logs" jsonschema:"newest first"`
	// NextCursor is absent once the window is exhausted.
	NextCursor string `json:"nextCursor,omitempty" jsonschema:"pass it back as cursor to read older lines"`
	Note       string `json:"note,omitempty"`
}

// truncate keeps a long body or attribute blob from eating the answer.
func truncate(value string) string {
	if len(value) <= maxLogFieldLength {
		return value
	}
	return value[:maxLogFieldLength] + "… (truncated)"
}

func queryLogsHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input QueryLogsInput) (*mcpprot.CallToolResult, QueryLogsOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input QueryLogsInput) (*mcpprot.CallToolResult, QueryLogsOutput, error) {
		if err := deps.requireTelemetry(ctx, req, input.AppId); err != nil {
			return nil, QueryLogsOutput{}, err
		}
		severity := strings.ToLower(strings.TrimSpace(input.Severity))
		switch severity {
		case "", "debug", "info", "warn", "error", "fatal":
		default:
			return nil, QueryLogsOutput{}, errors.New("severity must be debug, info, warn, error or fatal")
		}
		if err := input.ObserveFilters.rejectConditions(); err != nil {
			return nil, QueryLogsOutput{}, err
		}
		if len(input.Search) > 256 {
			return nil, QueryLogsOutput{}, errors.New("search must be at most 256 characters")
		}
		explorerQuery, err := deps.explorerQuery(ctx, input.AppId, input.ObserveFilters, input.From, input.To, maxLogsWindow)
		if err != nil {
			return nil, QueryLogsOutput{}, err
		}
		cursor, err := observe.DecodeLogCursor(input.Cursor)
		if err != nil {
			return nil, QueryLogsOutput{}, errors.New("cursor is invalid; pass a nextCursor from a previous answer")
		}
		limit := input.Limit
		if limit <= 0 {
			limit = defaultLogsPage
		}
		if limit > maxLogsPage {
			limit = maxLogsPage
		}

		ctx, cancel := boundedRead(ctx)
		defer cancel()
		page, err := deps.Explorer.ReadLogs(ctx, input.AppId, observe.LogsQuery{
			ExplorerQuery: explorerQuery,
			Severity:      severity,
			Search:        strings.TrimSpace(input.Search),
			EventNames:    input.EventNames,
			Cursor:        cursor,
			Limit:         limit,
		})
		if err != nil {
			log.Printf("mcp query_logs failed for app %s: %v", input.AppId, err)
			return nil, QueryLogsOutput{}, errors.New("could not read the logs, try again later")
		}

		output := QueryLogsOutput{
			Available:  page.Available,
			Logs:       make([]LogOutput, 0, len(page.Logs)),
			NextCursor: page.NextCursor,
		}
		if !page.Available {
			output.Note = "this deployment has no ClickHouse configured, so logs and crashes are not stored"
		}
		for _, line := range page.Logs {
			output.Logs = append(output.Logs, LogOutput{
				Timestamp:   line.Timestamp.UTC().Format(time.RFC3339),
				EventName:   line.EventName,
				Severity:    line.SeverityText,
				IsFatal:     line.IsFatal,
				Body:        truncate(line.Body),
				Attributes:  truncate(line.Attributes),
				EasClientId: line.EASClientID,
				SessionId:   line.SessionID,
				UpdateId:    line.UpdateID,
				Branch:      line.Branch,
				Channel:     line.Channel,
				Platform:    line.Platform,
				OsVersion:   line.OSVersion,
				DeviceModel: line.DeviceModel,
				AppVersion:  line.AppVersion,
			})
		}
		return nil, output, nil
	}
}

func registerQueryLogs(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name: "query_logs",
		Description: "The log and crash stream of an app, newest first, paginated backwards by cursor. Narrow with severity, a text search, exact event names, and any device or release filter. " +
			"severity selects one band and excludes the others, so leave it unset to see everything and pass error or fatal to hunt failures; fatal also surfaces native launch crashes, which carry no attributes. Requires the observe:read permission.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Query logs", ReadOnlyHint: true},
	}, queryLogsHandler(deps))
}
