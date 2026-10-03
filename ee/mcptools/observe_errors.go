// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	"errors"
	"log"
	"strings"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
	"xprem/ee/licensing"
	"xprem/ee/observe"
)

type QueryErrorsInput struct {
	AppId string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	ObserveFilters
	From          string `json:"from,omitempty" jsonschema:"start of the window, RFC3339; up to 31 days"`
	To            string `json:"to,omitempty" jsonschema:"end of the window, RFC3339; defaults to now"`
	Search        string `json:"search,omitempty" jsonschema:"search the error type, message and culprit; at most 256 characters"`
	Fatality      string `json:"fatality,omitempty" jsonschema:"all, fatal or non_fatal; defaults to all"`
	Sort          string `json:"sort,omitempty" jsonschema:"occurrences, impactedDevices or lastSeen; defaults to occurrences"`
	Limit         int    `json:"limit,omitempty" jsonschema:"groups per page; default 50, max 100"`
	Offset        int    `json:"offset,omitempty" jsonschema:"groups to skip; at most 10000"`
	IncludeSeries bool   `json:"includeSeries,omitempty" jsonschema:"include occurrence histograms; defaults to false"`
}

type GetErrorDetailsInput struct {
	AppId   string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	ErrorId string `json:"errorId" jsonschema:"opaque group identity returned by query_errors"`
	ObserveFilters
	From          string `json:"from,omitempty" jsonschema:"start of the window, RFC3339; up to 31 days"`
	To            string `json:"to,omitempty" jsonschema:"end of the window, RFC3339; defaults to now"`
	Fatality      string `json:"fatality,omitempty" jsonschema:"all, fatal or non_fatal; defaults to all"`
	Limit         int    `json:"limit,omitempty" jsonschema:"occurrences per page; default 50, max 100"`
	Cursor        string `json:"cursor,omitempty" jsonschema:"nextCursor returned by the preceding details page"`
	IncludeSeries bool   `json:"includeSeries,omitempty" jsonschema:"include the complete occurrence histogram; defaults to false"`
}

// Each execution rechecks app permission, telemetry and the active license;
// tool registration and a reader's cache cannot authorize a later call.
func (deps Deps) requireErrorsTelemetry(ctx context.Context, req *mcpprot.CallToolRequest, appID string) error {
	if err := deps.requireTelemetry(ctx, req, appID); err != nil {
		return err
	}
	valid := deps.LicenseValid
	if valid == nil {
		valid = licensing.IsEnterprise
	}
	if !valid() {
		return errors.New("error tracking requires a valid Enterprise license")
	}
	return nil
}

func errorFatality(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all":
		return "all", nil
	case "fatal":
		return "fatal", nil
	case "nonfatal", "non_fatal":
		return "nonfatal", nil
	default:
		return "", errors.New("fatality must be all, fatal or non_fatal")
	}
}

func errorPageLimit(value int) (int, error) {
	if value == 0 {
		return 50, nil
	}
	if value < 1 || value > 100 {
		return 0, errors.New("limit must be between 1 and 100")
	}
	return value, nil
}

func queryErrorsHandler(deps Deps) func(context.Context, *mcpprot.CallToolRequest, QueryErrorsInput) (*mcpprot.CallToolResult, observe.ErrorsPage, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input QueryErrorsInput) (*mcpprot.CallToolResult, observe.ErrorsPage, error) {
		if err := deps.requireErrorsTelemetry(ctx, req, input.AppId); err != nil {
			return nil, observe.ErrorsPage{}, err
		}
		if err := input.ObserveFilters.rejectConditions(); err != nil {
			return nil, observe.ErrorsPage{}, err
		}
		query, err := deps.explorerQuery(ctx, input.AppId, input.ObserveFilters, input.From, input.To, observe.ErrorsMaxWindow)
		if err != nil {
			return nil, observe.ErrorsPage{}, err
		}
		fatality, err := errorFatality(input.Fatality)
		if err != nil {
			return nil, observe.ErrorsPage{}, err
		}
		search := strings.TrimSpace(input.Search)
		if len(search) > 256 {
			return nil, observe.ErrorsPage{}, errors.New("search must be at most 256 characters")
		}
		sort := input.Sort
		if sort == "" {
			sort = "occurrences"
		}
		switch sort {
		case "occurrences", "impactedDevices", "lastSeen":
		default:
			return nil, observe.ErrorsPage{}, errors.New("sort must be occurrences, impactedDevices or lastSeen")
		}
		limit, err := errorPageLimit(input.Limit)
		if err != nil {
			return nil, observe.ErrorsPage{}, err
		}
		if input.Offset < 0 || input.Offset > 10000 {
			return nil, observe.ErrorsPage{}, errors.New("offset must be between 0 and 10000")
		}
		ctx, cancel := boundedRead(ctx)
		defer cancel()
		page, err := deps.Explorer.ReadErrors(ctx, input.AppId, observe.ErrorsQuery{
			ExplorerQuery: query, Search: search, Fatality: fatality, Sort: sort,
			Limit: limit, Offset: input.Offset, IncludeSeries: input.IncludeSeries,
		})
		if err != nil {
			log.Printf("mcp query_errors failed for app %s: %v", input.AppId, err)
			return nil, observe.ErrorsPage{}, errors.New("could not read errors, try again later")
		}
		return nil, page, nil
	}
}

func getErrorDetailsHandler(deps Deps) func(context.Context, *mcpprot.CallToolRequest, GetErrorDetailsInput) (*mcpprot.CallToolResult, observe.ErrorDetails, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetErrorDetailsInput) (*mcpprot.CallToolResult, observe.ErrorDetails, error) {
		if err := deps.requireErrorsTelemetry(ctx, req, input.AppId); err != nil {
			return nil, observe.ErrorDetails{}, err
		}
		if err := input.ObserveFilters.rejectConditions(); err != nil {
			return nil, observe.ErrorDetails{}, err
		}
		query, err := deps.explorerQuery(ctx, input.AppId, input.ObserveFilters, input.From, input.To, observe.ErrorsMaxWindow)
		if err != nil {
			return nil, observe.ErrorDetails{}, err
		}
		fatality, err := errorFatality(input.Fatality)
		if err != nil {
			return nil, observe.ErrorDetails{}, err
		}
		limit, err := errorPageLimit(input.Limit)
		if err != nil {
			return nil, observe.ErrorDetails{}, err
		}
		if len(input.Cursor) > 512 {
			return nil, observe.ErrorDetails{}, errors.New("cursor is invalid")
		}
		cursor, err := observe.DecodeLogCursor(input.Cursor)
		if err != nil {
			return nil, observe.ErrorDetails{}, errors.New("cursor is invalid")
		}
		if input.ErrorId == "" || len(input.ErrorId) > 8192 {
			return nil, observe.ErrorDetails{}, errors.New("errorId is invalid")
		}
		ctx, cancel := boundedRead(ctx)
		defer cancel()
		details, err := deps.Explorer.ReadErrorDetails(ctx, input.AppId, input.ErrorId, observe.ErrorDetailsQuery{
			ExplorerQuery: query, Fatality: fatality, Limit: limit, Cursor: cursor,
		})
		if err != nil {
			if errors.Is(err, observe.ErrInvalidErrorID) {
				return nil, observe.ErrorDetails{}, errors.New("errorId is invalid")
			}
			log.Printf("mcp get_error_details failed for app %s: %v", input.AppId, err)
			return nil, observe.ErrorDetails{}, errors.New("could not read error details, try again later")
		}
		if !input.IncludeSeries {
			details.Series = nil
		}
		return nil, details, nil
	}
}

func registerQueryErrors(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name: "query_errors", Description: "List error groups with distinct occurrences, impacted installations and fatal occurrence counts. Filter and paginate over at most 31 days; histograms are opt-in. Requires observe:read on the application and a valid Enterprise license. Includes fatal errors in Observe logs; launch crashes from device health events are not included.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Telemetry errors", ReadOnlyHint: true},
	}, queryErrorsHandler(deps))
}

func registerGetErrorDetails(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name: "get_error_details", Description: "Read an error group's counts, server-calculated breakdown percentages, precise occurrences and their original update and fingerprint for symbolication. Use the same filters and window as query_errors; occurrence pagination is bounded and histograms are opt-in. Requires observe:read on the application and a valid Enterprise license.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Error details", ReadOnlyHint: true},
	}, getErrorDetailsHandler(deps))
}
