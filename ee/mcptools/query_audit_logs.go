// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	"errors"
	"log"
	"time"

	"xprem/ee/audit"
	mittools "xprem/internal/mcptools"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultAuditLimit = 20
	maxAuditLimit     = 100
)

type QueryAuditLogsInput struct {
	ActorId  string `json:"actorId,omitempty" jsonschema:"filter by the acting user id or api key id"`
	Action   string `json:"action,omitempty" jsonschema:"filter by action name, e.g. update.published or user.created"`
	AppId    string `json:"appId,omitempty" jsonschema:"filter by app id, as returned by get_apps"`
	Outcome  string `json:"outcome,omitempty" jsonschema:"filter by outcome: success, denied or failure"`
	From     string `json:"from,omitempty" jsonschema:"only events at or after this RFC3339 timestamp"`
	To       string `json:"to,omitempty" jsonschema:"only events at or before this RFC3339 timestamp"`
	BeforeId int64  `json:"beforeId,omitempty" jsonschema:"page backwards: pass the nextCursor of a previous answer to fetch older events"`
	Limit    int    `json:"limit,omitempty" jsonschema:"maximum events returned; default 20, max 100"`
}

type AuditEventOutput struct {
	Id            int64          `json:"id"`
	OccurredAt    string         `json:"occurredAt"`
	ActorType     string         `json:"actorType,omitempty"`
	ActorId       string         `json:"actorId,omitempty"`
	ActorDisplay  string         `json:"actorDisplay,omitempty"`
	Action        string         `json:"action"`
	TargetType    string         `json:"targetType,omitempty"`
	TargetId      string         `json:"targetId,omitempty"`
	TargetDisplay string         `json:"targetDisplay,omitempty"`
	AppId         string         `json:"appId,omitempty"`
	Outcome       string         `json:"outcome,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

type QueryAuditLogsOutput struct {
	Events []AuditEventOutput `json:"events"`
	// NextCursor pages backwards: pass it as beforeId to fetch older events.
	NextCursor *int64 `json:"nextCursor,omitempty"`
}

func queryAuditLogsHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input QueryAuditLogsInput) (*mcpprot.CallToolResult, QueryAuditLogsOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input QueryAuditLogsInput) (*mcpprot.CallToolResult, QueryAuditLogsOutput, error) {
		principal := mittools.PrincipalFromRequest(req)
		if principal == nil {
			return nil, QueryAuditLogsOutput{}, errors.New("no authenticated account on this session")
		}
		// The audit log is the whole deployment's history: admin only, like
		// its dashboard route.
		if !principal.IsAdmin {
			return nil, QueryAuditLogsOutput{}, errors.New("the audit log requires an admin account")
		}

		limit := input.Limit
		if limit <= 0 {
			limit = defaultAuditLimit
		}
		if limit > maxAuditLimit {
			limit = maxAuditLimit
		}
		params := audit.ListParams{Limit: limit}
		for _, bound := range []struct {
			raw    string
			target **string
		}{
			{input.ActorId, &params.ActorID},
			{input.Action, &params.Action},
			{input.AppId, &params.AppID},
			{input.Outcome, &params.Outcome},
		} {
			if bound.raw != "" {
				value := bound.raw
				*bound.target = &value
			}
		}
		for _, bound := range []struct {
			raw    string
			target **time.Time
		}{{input.From, &params.From}, {input.To, &params.To}} {
			if bound.raw == "" {
				continue
			}
			parsed, err := time.Parse(time.RFC3339, bound.raw)
			if err != nil {
				return nil, QueryAuditLogsOutput{}, errors.New("from and to must be RFC3339 timestamps")
			}
			*bound.target = &parsed
		}
		if input.BeforeId > 0 {
			beforeId := input.BeforeId
			params.BeforeID = &beforeId
		}

		events, nextCursor, err := deps.Audit.List(ctx, params)
		if err != nil {
			if errors.Is(err, audit.ErrRequiresControlPlane) {
				return nil, QueryAuditLogsOutput{}, errors.New("the audit log requires the control plane; this deployment runs in stateless mode")
			}
			log.Printf("mcp query_audit_logs failed: %v", err)
			return nil, QueryAuditLogsOutput{}, errors.New("could not query the audit log, try again later")
		}
		output := QueryAuditLogsOutput{Events: make([]AuditEventOutput, len(events)), NextCursor: nextCursor}
		for i, event := range events {
			output.Events[i] = AuditEventOutput{
				Id:            event.ID,
				OccurredAt:    event.OccurredAt.UTC().Format(time.RFC3339),
				ActorType:     string(event.ActorType),
				ActorId:       event.ActorID,
				ActorDisplay:  event.ActorDisplay,
				Action:        string(event.Action),
				TargetType:    event.TargetType,
				TargetId:      event.TargetID,
				TargetDisplay: event.TargetDisplay,
				AppId:         event.AppID,
				Outcome:       string(event.Outcome),
				Metadata:      event.Metadata,
			}
		}
		return nil, output, nil
	}
}

func registerQueryAuditLogs(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "query_audit_logs",
		Description: "The deployment's audit log (admin only), newest first, max 100 per call. Narrow with actorId, action, appId, outcome, or a from/to range; page older events by passing nextCursor back as beforeId.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Audit log", ReadOnlyHint: true},
	}, queryAuditLogsHandler(deps))
}
