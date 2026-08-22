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

	"xprem/internal/handlers"

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
// requirement for the dashboard. Deployments without it fall back to what
// PostgreSQL's live state can reconstruct, and keep the instant-T health
// endpoint fully operational either way.
type HealthHistoryHandler struct {
	reader HealthHistoryReader
	state  *StateHistory
}

// healthHistoryResponse and stateHistoryResponse are the two shapes of the
// health history endpoint, told apart by Source; the dashboard reads them as a
// discriminated union.
type healthHistoryResponse struct {
	Available bool                            `json:"available"`
	Source    string                          `json:"source"`
	Updates   map[string][]HealthHistoryPoint `json:"updates"`
}

type stateHistoryResponse struct {
	Available bool                           `json:"available"`
	Source    string                         `json:"source"`
	Updates   map[string][]StateHistoryPoint `json:"updates"`
}

type healthSegmentsResponse struct {
	Available bool                            `json:"available"`
	Dimension string                          `json:"dimension"`
	Segments  map[string][]HealthSegmentPoint `json:"segments"`
}

func NewHealthHistoryHandler(reader HealthHistoryReader, state *StateHistory) *HealthHistoryHandler {
	// A nil *HealthHistory stored in an interface is itself non-nil. Wiring
	// does exactly that when ClickHouse is disabled, so normalize it here
	// before the handler uses the interface.
	if history, ok := reader.(*HealthHistory); ok && history == nil {
		reader = nil
	}
	return &HealthHistoryHandler{reader: reader, state: state}
}

// healthHistoryQuery is what both handlers below need before they can answer,
// and parsing it renders its own 400s. Shared because the two routes differ in
// what they read and in who may read it, not in how they are addressed.
type healthHistoryQuery struct {
	updateIDs []string
	from      time.Time
	to        time.Time
}

func parseHealthHistoryQuery(w http.ResponseWriter, r *http.Request) (healthHistoryQuery, bool) {
	updateIDs, ok := parseHealthHistoryUpdateIDs(r.URL.Query().Get("ids"))
	if !ok {
		handlers.RenderError(
			w,
			http.StatusBadRequest,
			"'ids' must contain between 1 and 20 unique update UUIDs.",
		)
		return healthHistoryQuery{}, false
	}

	to, err := parseHistoryTime(r.URL.Query().Get("to"), time.Now().UTC())
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "'to' must be an RFC3339 timestamp.")
		return healthHistoryQuery{}, false
	}
	from, err := parseHistoryTime(r.URL.Query().Get("from"), to.Add(-24*time.Hour))
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "'from' must be an RFC3339 timestamp.")
		return healthHistoryQuery{}, false
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
		return healthHistoryQuery{}, false
	}
	return healthHistoryQuery{updateIDs: updateIDs, from: from, to: to}, true
}

// GetUpdateHealthHistoryHandler answers one series per update: adoption and
// launch health over time, keyed by update id, naming no device. It feeds the
// updates table's adoption column and the rollout card's health graph, which
// is why the route is open to any account that can see the app.
func (h *HealthHistoryHandler) GetUpdateHealthHistoryHandler(w http.ResponseWriter, r *http.Request) {
	query, ok := parseHealthHistoryQuery(w, r)
	if !ok {
		return
	}

	readContext, cancelRead := boundedRead(r)
	defer cancelRead()

	// Without the projection, PostgreSQL still holds enough to draw something
	// true, and "source" says which of the two the caller got. It is not a
	// detail the caller may ignore: the two series answer different questions,
	// and the field names below differ for the same reason, so a client that
	// forgets to branch fails to read the payload rather than mislabelling it.
	if h.reader == nil {
		if h.state == nil {
			handlers.RenderJSON(w, http.StatusOK, healthHistoryResponse{Source: "none", Updates: map[string][]HealthHistoryPoint{}})
			return
		}
		points, err := h.state.Read(readContext, mux.Vars(r)["APP_ID"], query.updateIDs, query.from, query.to)
		if err != nil {
			handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
			return
		}
		handlers.RenderJSON(w, http.StatusOK, stateHistoryResponse{Available: true, Source: "state", Updates: points})
		return
	}

	points, err := h.reader.Read(readContext, mux.Vars(r)["APP_ID"], query.updateIDs, query.from, query.to)
	if err != nil {
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
		return
	}
	handlers.RenderJSON(w, http.StatusOK, healthHistoryResponse{Available: true, Source: "projected", Updates: points})
}

// GetUpdateHealthSegmentsHandler answers the same window split by a device
// dimension: the keys become segment values instead of update ids.
//
// A separate route rather than a mode of the one above, and the reason is
// worth stating because it used to be one route. It reads different data
// (raw device_health_events rebuilt into a device-by-bucket grid, not the
// per-update snapshots), returns a different shape, serves a different screen,
// and answers to a different permission: the dimensions on offer are
// device_model, os_version, country_code and their neighbours, the very
// columns /observe/breakdown groups by under observe:read. As one route that
// last part could only be enforced inside the handler, where nothing makes
// anyone remember it. As two, it is a line in the routing table like every
// other permission in this codebase.
func (h *HealthHistoryHandler) GetUpdateHealthSegmentsHandler(w http.ResponseWriter, r *http.Request) {
	query, ok := parseHealthHistoryQuery(w, r)
	if !ok {
		return
	}

	dimension := strings.TrimSpace(r.URL.Query().Get("dimension"))
	if !IsHealthSegmentDimension(dimension) {
		handlers.RenderError(w, http.StatusBadRequest, "'dimension' cannot split health history.")
		return
	}

	// Unavailable still answers in the shape that was asked for: this caller
	// reads `segments`, and finding the key missing entirely is a different
	// failure from finding it empty.
	if h.reader == nil {
		handlers.RenderJSON(w, http.StatusOK, healthSegmentsResponse{Dimension: dimension, Segments: map[string][]HealthSegmentPoint{}})
		return
	}

	readContext, cancelRead := boundedRead(r)
	defer cancelRead()
	segments, err := h.reader.ReadBySegment(
		readContext, mux.Vars(r)["APP_ID"], query.updateIDs, dimension, query.from, query.to,
	)
	if err != nil {
		log.Printf("observe: reading segmented health history failed: %v", err)
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
		return
	}
	handlers.RenderJSON(w, http.StatusOK, healthSegmentsResponse{Available: true, Dimension: dimension, Segments: TrimSegments(segments, maxHealthSegments)})
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
