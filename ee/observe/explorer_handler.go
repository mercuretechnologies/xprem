// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"expo-open-ota/ee/identity"
	"expo-open-ota/internal/handlers"
)

var (
	errInvalidObserveRange    = errors.New("invalid Observe time range")
	errInvalidObservePlatform = errors.New("invalid Observe platform")
	errInvalidObserveFilter   = errors.New("invalid Observe filter")
	errInvalidIdentityFilter  = errors.New("invalid Identity filter")
	// The identity cohort behind an attribute filter is copied onto the wire
	// as an external table on every request, so it has a hard cap. Past it the
	// only honest answers are "narrow the filter" or a silently partial
	// result; this is the first one.
	errObserveCohortTooLarge = errors.New("Observe identity cohort too large")
)

const (
	defaultObserveWindow = 30 * 24 * time.Hour
	maxOverviewWindow    = 90 * 24 * time.Hour
	maxLogsWindow        = 31 * 24 * time.Hour
	defaultLogsLimit     = 200
	maxLogsLimit         = 500
	// A check-in poll is a tail read, and its cost is the number of registry
	// rows inside the window, not the size of the fleet as such. The recorder
	// debounces to one write per device per minute, so a window of W seconds
	// touches roughly (concurrently active devices * W / 60) rows: on a fleet
	// with 500k devices online, 30 seconds is already a quarter million rows
	// to aggregate. So the lookback is clamped hard, and a client that fell
	// behind (backgrounded tab, slow network) skips the gap instead of paying
	// for it. It loses nothing that matters: the missed arrivals are already
	// counted in the overview's static layer, only their animation is gone.
	maxCheckInLookback     = 30 * time.Second
	defaultCheckInLookback = 10 * time.Second
)

type ExplorerReader interface {
	ReadOverview(ctx context.Context, appID string, query ExplorerQuery) (Overview, error)
	ReadCheckIns(ctx context.Context, appID string, query CheckInQuery) (CheckInFeed, error)
	ReadSummary(ctx context.Context, appID string, query ExplorerQuery) (Summary, error)
	ReadEvents(ctx context.Context, appID string, query ExplorerQuery) (Events, error)
	ReadLogs(ctx context.Context, appID string, query LogsQuery) (LogsPage, error)
	ReadBreakdown(ctx context.Context, appID string, query BreakdownQuery) (Breakdown, error)
}

type IdentitySchemaReader interface {
	GetSchema(ctx context.Context, appID string) (identity.Schema, error)
}

type ExplorerHandler struct {
	reader ExplorerReader
	schema IdentitySchemaReader
}

func NewExplorerHandler(reader ExplorerReader, schema IdentitySchemaReader) *ExplorerHandler {
	// A nil *Explorer or *identity.Service stored in an interface is itself
	// non-nil. Wiring does exactly that when ClickHouse or the control plane is
	// disabled, so normalize both here before the handler uses the interfaces.
	if explorer, ok := reader.(*Explorer); ok && explorer == nil {
		reader = nil
	}
	if service, ok := schema.(*identity.Service); ok && service == nil {
		schema = nil
	}
	return &ExplorerHandler{reader: reader, schema: schema}
}

func parseExplorerTimes(values map[string][]string, maximum time.Duration) (time.Time, time.Time, error) {
	to, err := parseHistoryTime(firstValue(values, "to"), time.Now().UTC())
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	from, err := parseHistoryTime(firstValue(values, "from"), to.Add(-defaultObserveWindow))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !from.Before(to) || to.Sub(from) > maximum {
		return time.Time{}, time.Time{}, strconv.ErrRange
	}
	return from.UTC(), to.UTC(), nil
}

func firstValue(values map[string][]string, key string) string {
	if entries := values[key]; len(entries) > 0 {
		return entries[0]
	}
	return ""
}

func observeBucket(window time.Duration) time.Duration {
	switch {
	case window <= 6*time.Hour:
		return 5 * time.Minute
	case window <= 24*time.Hour:
		return 15 * time.Minute
	case window <= 7*24*time.Hour:
		return time.Hour
	case window <= 30*24*time.Hour:
		return 6 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// Repeated parameters, not a separator: an Apple hardware identifier is
// "iPhone18,2", so splitting on a comma would turn one model into two, and any
// other separator is a bet that no value will ever contain it.
func splitFilterValues(raw []string) []string {
	values := make([]string, 0, len(raw))
	for _, part := range raw {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

const maxFilterValues = 25

func parseTextFilters(raw []string) ([]string, bool) {
	values := splitFilterValues(raw)
	if len(values) > maxFilterValues {
		return nil, false
	}
	for _, value := range values {
		if len(value) > 256 {
			return nil, false
		}
	}
	return values, true
}

func parseUUIDFilters(raw []string) ([]string, bool) {
	values := splitFilterValues(raw)
	if len(values) > maxFilterValues {
		return nil, false
	}
	parsed := make([]string, 0, len(values))
	for _, value := range values {
		id, err := uuid.Parse(value)
		if err != nil {
			return nil, false
		}
		parsed = append(parsed, id.String())
	}
	return parsed, true
}

// Returns the normalized values, not the ones that came in: platform is stored
// lowercase, so passing "IOS" straight through validates fine and then matches
// nothing, which reads as "no data" instead of as a rejected filter.
func parsePlatformFilters(raw []string) ([]string, bool) {
	values := splitFilterValues(raw)
	// Capped like every other dimension. The values are validated against a
	// two-entry allowlist below, but the LIST is not: repeating ?platform=ios
	// builds an arbitrarily long IN clause out of one legal value.
	if len(values) > maxFilterValues {
		return nil, false
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		platform, ok := parsePlatform(value)
		if !ok {
			return nil, false
		}
		normalized = append(normalized, platform)
	}
	return normalized, true
}

func parsePlatform(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "ios", "android":
		return value, true
	default:
		return "", false
	}
}

// Attributes arrive as `key:value` pairs. Repeating a key widens it ("plan is
// pro or enterprise"), naming another key narrows further ("and tenant is
// globex"), and the pair set becomes the containment documents the cohort
// lookup runs on.
func (h *ExplorerHandler) metadataFilter(ctx context.Context, appID string, pairs []string) ([][]byte, error) {
	pairs, ok := parseTextFilters(pairs)
	if !ok {
		return nil, errInvalidIdentityFilter
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	if h.schema == nil {
		return nil, errInvalidIdentityFilter
	}
	schema, err := h.schema.GetSchema(ctx, appID)
	if err != nil {
		return nil, err
	}
	filters, err := identity.ParseFilterPairs(schema, pairs)
	if err != nil {
		return nil, errInvalidIdentityFilter
	}
	docs, err := filters.ContainmentDocs()
	if err != nil {
		return nil, errInvalidIdentityFilter
	}
	return docs, nil
}

func (h *ExplorerHandler) parseBaseQuery(r *http.Request, maximum time.Duration) (ExplorerQuery, error) {
	from, to, err := parseExplorerTimes(r.URL.Query(), maximum)
	if err != nil {
		return ExplorerQuery{}, errInvalidObserveRange
	}
	query, err := h.parseDimensions(r)
	if err != nil {
		return ExplorerQuery{}, err
	}
	query.From = from
	query.To = to
	query.Bucket = observeBucket(to.Sub(from))
	return query, nil
}

// The narrowing half of a query, without its window. Split out because the map
// feed bounds itself with a cursor rather than a period, and still has to
// honour every dimension the page is filtered on.
func (h *ExplorerHandler) parseDimensions(r *http.Request) (ExplorerQuery, error) {
	platforms, ok := parsePlatformFilters(r.URL.Query()["platform"])
	if !ok {
		return ExplorerQuery{}, errInvalidObservePlatform
	}
	filter, err := h.metadataFilter(r.Context(), mux.Vars(r)["APP_ID"], r.URL.Query()["attr"])
	if err != nil {
		return ExplorerQuery{}, err
	}

	// One value out of range on any dimension rejects the whole request, so the
	// failure is held and checked once. That keeps every parameter name next to
	// the field it fills: routing them through two maps and reading them back by
	// string key put a typo's worth of distance between the two halves.
	invalid := false
	uuidFilter := func(name string) []string {
		values, ok := parseUUIDFilters(r.URL.Query()[name])
		invalid = invalid || !ok
		return values
	}
	textFilter := func(name string) []string {
		values, ok := parseTextFilters(r.URL.Query()[name])
		invalid = invalid || !ok
		return values
	}

	// Each condition travels under its own dimension name, so the parameter a
	// breakdown row is split by is the parameter clicking it filters on.
	conditions := map[string][]string{}
	for _, name := range ConditionDimensions() {
		if values := textFilter(name); len(values) > 0 {
			conditions[name] = values
		}
	}

	query := ExplorerQuery{
		Platform:        platforms,
		UpdateIDs:       uuidFilter("updateId"),
		UpdateGroupIDs:  uuidFilter("updateGroupId"),
		EASClientIDs:    uuidFilter("easClientId"),
		EASBuildIDs:     uuidFilter("easBuildId"),
		Branches:        textFilter("branch"),
		RuntimeVersions: textFilter("runtimeVersion"),
		Channels:        textFilter("channel"),
		AppVersions:     textFilter("appVersion"),
		AppBuildNumbers: textFilter("appBuildNumber"),
		Environments:    textFilter("environment"),
		OSNames:         textFilter("osName"),
		OSVersions:      textFilter("osVersion"),
		DeviceModels:    textFilter("deviceModel"),
		CountryCodes:    textFilter("countryCode"),
		MetadataFilter:  filter,
		Conditions:      conditions,
	}
	if invalid {
		return ExplorerQuery{}, errInvalidObserveFilter
	}
	return query, nil
}

// GetConditionsHandler serves the conditions a timing can be filtered on and
// the values each one takes. Served rather than restated in the dashboard
// because the ranges are cut here: a bucket renamed on this side would
// otherwise leave a picker offering a range no query can match any more.
func (h *ExplorerHandler) GetConditionsHandler(w http.ResponseWriter, _ *http.Request) {
	handlers.RenderJSON(w, http.StatusOK, ObserveConditions())
}

func (h *ExplorerHandler) renderQueryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidObserveRange):
		handlers.RenderError(w, http.StatusBadRequest, "'from' and 'to' must define a valid bounded RFC3339 range.")
	case errors.Is(err, errInvalidObservePlatform):
		handlers.RenderError(w, http.StatusBadRequest, "'platform' must be ios or android.")
	case errors.Is(err, errInvalidObserveFilter):
		handlers.RenderError(w, http.StatusBadRequest, "One or more Observe filters are invalid.")
	case errors.Is(err, errInvalidIdentityFilter):
		handlers.RenderError(w, http.StatusBadRequest, "The Identity filter is invalid.")
	case errors.Is(err, errObserveCohortTooLarge):
		handlers.RenderError(w, http.StatusBadRequest, "The attribute filter matches too many devices. Narrow it down.")
	default:
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
	}
}

func (h *ExplorerHandler) GetEventsHandler(w http.ResponseWriter, r *http.Request) {
	query, err := h.parseBaseQuery(r, maxOverviewWindow)
	if err != nil {
		h.renderQueryError(w, err)
		return
	}
	if h.reader == nil {
		handlers.RenderJSON(w, http.StatusOK, Events{Available: false, Events: []ObserveEventSeries{}})
		return
	}
	events, err := h.reader.ReadEvents(r.Context(), mux.Vars(r)["APP_ID"], query)
	if err != nil {
		log.Printf("observe: reading events failed: %v", err)
		h.renderQueryError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, events)
}

func (h *ExplorerHandler) GetOverviewHandler(w http.ResponseWriter, r *http.Request) {
	query, err := h.parseBaseQuery(r, maxOverviewWindow)
	if err != nil {
		h.renderQueryError(w, err)
		return
	}
	if h.reader == nil {
		handlers.RenderJSON(w, http.StatusOK, Overview{
			Available: false,
			Metrics:   emptyMetricSeries(),
			Locations: []ObserveLocation{},
		})
		return
	}
	overview, err := h.reader.ReadOverview(r.Context(), mux.Vars(r)["APP_ID"], query)
	if err != nil {
		log.Printf("observe: reading overview failed: %v", err)
		h.renderQueryError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, overview)
}

// GetCheckInsHandler feeds the live map. The client sends back the cursor it
// got last time and receives everything that checked in since, so the two ends
// never disagree about where the window starts.
func (h *ExplorerHandler) GetCheckInsHandler(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	since, err := parseHistoryTime(r.URL.Query().Get("since"), now.Add(-defaultCheckInLookback))
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "'since' must be an RFC3339 timestamp.")
		return
	}
	since = since.UTC()
	if floor := now.Add(-maxCheckInLookback); since.Before(floor) {
		since = floor
	}
	if since.After(now) {
		since = now
	}
	dimensions, err := h.parseDimensions(r)
	if err != nil {
		h.renderQueryError(w, err)
		return
	}
	if h.reader == nil {
		handlers.RenderJSON(w, http.StatusOK, CheckInFeed{Cities: []ObserveLocation{}, Cursor: now})
		return
	}
	feed, err := h.reader.ReadCheckIns(
		r.Context(),
		mux.Vars(r)["APP_ID"],
		CheckInQuery{Since: since, Dimensions: dimensions},
	)
	if err != nil {
		log.Printf("observe: reading check-ins failed: %v", err)
		h.renderQueryError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, feed)
}

func (h *ExplorerHandler) GetSummaryHandler(w http.ResponseWriter, r *http.Request) {
	query, err := h.parseBaseQuery(r, maxOverviewWindow)
	if err != nil {
		h.renderQueryError(w, err)
		return
	}
	if h.reader == nil {
		handlers.RenderJSON(w, http.StatusOK, Summary{Available: false})
		return
	}
	summary, err := h.reader.ReadSummary(r.Context(), mux.Vars(r)["APP_ID"], query)
	if err != nil {
		log.Printf("observe: reading summary failed: %v", err)
		h.renderQueryError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, summary)
}

// A breakdown is the "who is this slow for" question: one metric split by one
// dimension, ranked by volume. The dimension is validated against the reader's
// allowlist here so an unknown value fails as a 400 rather than reaching the
// query builder.
func (h *ExplorerHandler) GetBreakdownHandler(w http.ResponseWriter, r *http.Request) {
	base, err := h.parseBaseQuery(r, maxOverviewWindow)
	if err != nil {
		h.renderQueryError(w, err)
		return
	}
	// Comma-separated: overlaying "device and country" is one grouping, not
	// two requests the dashboard would have to stitch back together.
	dimensions := []string{}
	for _, name := range strings.Split(r.URL.Query().Get("dimension"), ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !IsBreakdownDimension(name) {
			handlers.RenderError(w, http.StatusBadRequest, "'dimension' is not a known Observe dimension.")
			return
		}
		dimensions = append(dimensions, name)
	}
	if len(dimensions) == 0 || len(dimensions) > maxBreakdownDimensions {
		handlers.RenderError(w, http.StatusBadRequest, "'dimension' must name between one and three dimensions.")
		return
	}
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		handlers.RenderError(w, http.StatusBadRequest, "'metric' is required.")
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxBreakdownSegments {
			handlers.RenderError(w, http.StatusBadRequest, "'limit' is out of range.")
			return
		}
	}
	if h.reader == nil {
		handlers.RenderJSON(w, http.StatusOK, Breakdown{
			Available:  false,
			Metric:     metric,
			Dimensions: dimensions,
			Segments:   []BreakdownSegment{},
			// Same shape as a real answer, so a caller can read the baseline
			// without checking Available first.
			Overall: BreakdownSegment{Values: []string{}},
		})
		return
	}
	breakdown, err := h.reader.ReadBreakdown(r.Context(), mux.Vars(r)["APP_ID"], BreakdownQuery{
		ExplorerQuery: base,
		Metric:        metric,
		Dimensions:    dimensions,
		Limit:         limit,
		WithPoints:    r.URL.Query().Get("points") == "1",
	})
	if err != nil {
		if errors.Is(err, errInvalidObserveFilter) {
			handlers.RenderError(w, http.StatusBadRequest, "'metric' is not a known Observe metric.")
			return
		}
		log.Printf("observe: reading breakdown failed: %v", err)
		h.renderQueryError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, breakdown)
}

func (h *ExplorerHandler) GetLogsHandler(w http.ResponseWriter, r *http.Request) {
	base, err := h.parseBaseQuery(r, maxLogsWindow)
	if err != nil {
		h.renderQueryError(w, err)
		return
	}
	severity := strings.ToLower(r.URL.Query().Get("severity"))
	switch severity {
	case "", "debug", "info", "warn", "error", "fatal":
	default:
		handlers.RenderError(w, http.StatusBadRequest, "'severity' is invalid.")
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if len(search) > 256 {
		handlers.RenderError(w, http.StatusBadRequest, "'search' must be at most 256 characters.")
		return
	}
	eventNames, ok := parseTextFilters(r.URL.Query()["eventName"])
	if !ok {
		handlers.RenderError(w, http.StatusBadRequest, "'eventName' is invalid.")
		return
	}
	cursor, err := DecodeLogCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "'cursor' is invalid.")
		return
	}
	limit := defaultLogsLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxLogsLimit {
			handlers.RenderError(w, http.StatusBadRequest, "'limit' must be between 1 and 500.")
			return
		}
	}
	if h.reader == nil {
		handlers.RenderJSON(w, http.StatusOK, LogsPage{Available: false, Logs: []ObserveLog{}})
		return
	}
	page, err := h.reader.ReadLogs(r.Context(), mux.Vars(r)["APP_ID"], LogsQuery{
		ExplorerQuery: base,
		Severity:      severity,
		Search:        search,
		EventNames:    eventNames,
		Cursor:        cursor,
		Limit:         limit,
	})
	if err != nil {
		log.Printf("observe: reading logs failed: %v", err)
		h.renderQueryError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, page)
}
