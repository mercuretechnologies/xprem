// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"expo-open-ota/internal/handlers"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

const maxHealthHistoryUpdateIDs = 20

type HealthHistoryReader interface {
	Read(
		ctx context.Context,
		appID string,
		updateIDs []string,
		from, to time.Time,
	) (map[string][]HealthHistoryPoint, error)
	// ReadBySegment answers the same question split by a device dimension,
	// rebuilt from the raw events because the snapshots are pre-aggregated per
	// update and carry nothing about the device.
	ReadBySegment(
		ctx context.Context,
		appID string,
		updateIDs []string,
		dimension string,
		from, to time.Time,
	) (map[string][]HealthSegmentPoint, error)
}

// Same ceiling as the explorer's overview window: past it the chart is one
// pixel per day and the query is a table scan.
const maxHealthHistoryWindow = 90 * 24 * time.Hour

// HealthHistoryHandler exposes ClickHouse history without making ClickHouse a
// requirement for the dashboard. Deployments without it return available=false
// and keep the PostgreSQL instant-T health endpoint fully operational.
type HealthHistoryHandler struct {
	reader HealthHistoryReader
}

func NewHealthHistoryHandler(reader HealthHistoryReader) *HealthHistoryHandler {
	// A nil *HealthHistory stored in an interface is itself non-nil. Wiring
	// does exactly that when ClickHouse is disabled, so normalize it here
	// before the handler uses the interface.
	if history, ok := reader.(*HealthHistory); ok && history == nil {
		reader = nil
	}
	return &HealthHistoryHandler{reader: reader}
}

func (h *HealthHistoryHandler) GetUpdateHealthHistoryHandler(w http.ResponseWriter, r *http.Request) {
	updateIDs, ok := parseHealthHistoryUpdateIDs(r.URL.Query().Get("ids"))
	if !ok {
		handlers.RenderError(
			w,
			http.StatusBadRequest,
			"'ids' must contain between 1 and 20 unique update UUIDs.",
		)
		return
	}

	to, err := parseHistoryTime(r.URL.Query().Get("to"), time.Now().UTC())
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "'to' must be an RFC3339 timestamp.")
		return
	}
	from, err := parseHistoryTime(r.URL.Query().Get("from"), to.Add(-24*time.Hour))
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "'from' must be an RFC3339 timestamp.")
		return
	}
	// Bounded like the explorer's windows are. A split rebuilds a
	// device-by-bucket grid from raw events, so an unbounded 'from' is a full
	// scan of the telemetry table joined against the whole event history.
	if !from.Before(to) || to.Sub(from) > maxHealthHistoryWindow {
		handlers.RenderError(
			w,
			http.StatusBadRequest,
			"'from' must be earlier than 'to', and within 90 days of it.",
		)
		return
	}

	dimension := strings.TrimSpace(r.URL.Query().Get("dimension"))
	if dimension != "" && !IsHealthSegmentDimension(dimension) {
		handlers.RenderError(w, http.StatusBadRequest, "'dimension' cannot split health history.")
		return
	}

	// Unavailable still answers in the shape that was asked for: a caller that
	// requested a split reads `segments`, and finding the key missing entirely
	// is a different failure from finding it empty.
	if h.reader == nil {
		if dimension != "" {
			handlers.RenderJSON(w, http.StatusOK, map[string]any{
				"available": false,
				"dimension": dimension,
				"segments":  map[string][]HealthSegmentPoint{},
			})
			return
		}
		handlers.RenderJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"updates":   map[string][]HealthHistoryPoint{},
		})
		return
	}

	// Split requested: the response keys become segment values instead of
	// update ids, and the caller knows which it asked for.
	if dimension != "" {
		readContext, cancelRead := boundedRead(r)
		defer cancelRead()
		segments, err := h.reader.ReadBySegment(
			readContext, mux.Vars(r)["APP_ID"], updateIDs, dimension, from, to,
		)
		if err != nil {
			log.Printf("observe: reading segmented health history failed: %v", err)
			handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
			return
		}
		handlers.RenderJSON(w, http.StatusOK, map[string]any{
			"available": true,
			"dimension": dimension,
			"segments":  TrimSegments(segments, maxHealthSegments),
		})
		return
	}

	readContext, cancelRead := boundedRead(r)
	defer cancelRead()
	points, err := h.reader.Read(readContext, mux.Vars(r)["APP_ID"], updateIDs, from, to)
	if err != nil {
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
		return
	}
	handlers.RenderJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"updates":   points,
	})
}

func parseHealthHistoryUpdateIDs(raw string) ([]string, bool) {
	seen := make(map[string]struct{})
	updateIDs := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		updateID := strings.TrimSpace(part)
		parsed, err := uuid.Parse(updateID)
		if err != nil {
			return nil, false
		}
		canonical := parsed.String()
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		updateIDs = append(updateIDs, canonical)
		if len(updateIDs) > maxHealthHistoryUpdateIDs {
			return nil, false
		}
	}
	return updateIDs, len(updateIDs) > 0
}

func parseHistoryTime(raw string, fallback time.Time) (time.Time, error) {
	if raw == "" {
		return fallback, nil
	}
	return time.Parse(time.RFC3339, raw)
}
