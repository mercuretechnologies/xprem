// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package audit

import (
	"errors"
	"net/http"
	"strconv"
	"time"
	"xprem/internal/handlers"
)

type AuditHandler struct {
	service *AuditService
}

func NewAuditHandler(service *AuditService) *AuditHandler {
	return &AuditHandler{service: service}
}

func optStr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func checkAndParseLimit(limit *string) (int, error) {
	if limit == nil {
		return 0, nil
	}
	if n, err := strconv.Atoi(*limit); err == nil {
		return n, nil
	}
	return 0, &ValidationError{Message: "invalid limit"}
}

func checkAndParseBeforeID(beforeID *string) (*int64, error) {
	if beforeID == nil {
		return nil, nil
	}
	if n, err := strconv.ParseInt(*beforeID, 10, 64); err == nil {
		return &n, nil
	}
	return nil, &ValidationError{Message: "invalid beforeId"}
}

func checkAndParseRange(from *string, to *string) (*time.Time, *time.Time, error) {
	if from == nil && to == nil {
		return nil, nil, nil
	}
	var finalFrom *(time.Time)
	var finalTo *(time.Time)

	if from != nil {
		parsedFrom, errFrom := time.Parse(time.RFC3339, *from)
		if errFrom != nil {
			return nil, nil, &ValidationError{Message: "invalid from"}
		}
		finalFrom = &parsedFrom
	}
	if to != nil {
		parsedTo, errTo := time.Parse(time.RFC3339, *to)
		if errTo != nil {
			return nil, nil, &ValidationError{Message: "invalid to"}
		}
		finalTo = &parsedTo
	}
	if finalTo != nil && finalFrom != nil {
		notValid := finalTo.Before(*finalFrom)
		if notValid {
			return nil, nil, &ValidationError{Message: "invalid range: 'from' is after 'to'"}
		}
	}
	return finalFrom, finalTo, nil
}

type AuditEventResponse struct {
	Id            int64          `json:"id"`
	OccurredAt    string         `json:"occurredAt"`
	ActorType     string         `json:"actorType"`
	ActorId       string         `json:"actorId"`
	ActorDisplay  string         `json:"actorDisplay"`
	Action        string         `json:"action"`
	TargetType    string         `json:"targetType"`
	TargetId      string         `json:"targetId"`
	TargetDisplay string         `json:"targetDisplay"`
	AppId         string         `json:"appId,omitempty"`
	Outcome       string         `json:"outcome"`
	Ip            string         `json:"ip,omitempty"`
	UserAgent     string         `json:"userAgent,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

func auditEventResponseFrom(event Event) AuditEventResponse {
	return AuditEventResponse{
		Id: event.ID,
		// Normalized to UTC so the wire format doesn't depend on the server's timezone.
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
		Ip:            event.IP,
		UserAgent:     event.UserAgent,
		Metadata:      event.Metadata,
	}
}

type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

func renderAuditLogsServiceError(w http.ResponseWriter, err error) {
	validationErr := (*ValidationError)(nil)
	switch {
	case errors.Is(err, ErrRequiresControlPlane):
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &validationErr):
		handlers.RenderError(w, http.StatusBadRequest, validationErr.Error())
	default:
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
	}
}

type listAuditLogsResponse struct {
	Events     []AuditEventResponse `json:"events"`
	NextCursor *int64               `json:"nextCursor"`
	Count      int64                `json:"count"`
}

func (h *AuditHandler) ListAuditLogsHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	from, to, errRange := checkAndParseRange(
		optStr(query.Get("from")),
		optStr(query.Get("to")),
	)
	if errRange != nil {
		renderAuditLogsServiceError(w, errRange)
		return
	}
	beforeId, err := checkAndParseBeforeID(optStr(query.Get("beforeId")))
	if err != nil {
		renderAuditLogsServiceError(w, err)
		return
	}
	limit, err := checkAndParseLimit(optStr(query.Get("limit")))
	if err != nil {
		renderAuditLogsServiceError(w, err)
		return
	}
	params := ListParams{
		ListFilters: ListFilters{
			ActorID: optStr(query.Get("actorId")),
			Action:  optStr(query.Get("action")),
			AppID:   optStr(query.Get("appId")),
			Outcome: optStr(query.Get("outcome")),
			From:    from,
			To:      to,
		},
		BeforeID: beforeId,
		Limit:    limit,
	}

	events, nextCursor, err := h.service.List(r.Context(), params)
	if err != nil {
		renderAuditLogsServiceError(w, err)
		return
	}
	count, err := h.service.Count(r.Context(), params.ListFilters)
	if err != nil {
		renderAuditLogsServiceError(w, err)
		return
	}
	responseEvents := make([]AuditEventResponse, len(events))
	for i, event := range events {
		responseEvents[i] = auditEventResponseFrom(event)
	}
	handlers.RenderJSON(w, http.StatusOK, listAuditLogsResponse{Events: responseEvents, NextCursor: nextCursor, Count: count})
}
