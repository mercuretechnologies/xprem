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
	// errObserveCohortTooLarge is returned instead of a silently partial result.
	errObserveCohortTooLarge = errors.New("Observe identity cohort too large")
)

const (
	defaultObserveWindow = 30 * 24 * time.Hour
	maxOverviewWindow    = 90 * 24 * time.Hour
	maxLogsWindow        = 31 * 24 * time.Hour
	defaultLogsLimit     = 200
	maxLogsLimit         = 500
	// maxCheckInLookback clamps the tail-read window; a client that fell behind
	// skips the gap rather than paying for an unbounded aggregate.
	maxCheckInLookback     = 30 * time.Second
	defaultCheckInLookback = 10 * time.Second
)

type ExplorerReader interface {
	ReadOverview(ctx context.Context, appID string, query ExplorerQuery) (Overview, error)
	ReadCheckIns(ctx context.Context, appID string, query CheckInQuery) (CheckInFeed, error)
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
	// A nil *Explorer or *identity.Service stored in an interface is itself non-nil.
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

// Bucket is the granularity a window is read at. Exported because every
// surface asking for a series has to derive it the same way; the caller never
// picks it, so a wide window cannot ask for a million points.
func Bucket(window time.Duration) time.Duration {
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

func observeBucket(window time.Duration) time.Duration {
	return Bucket(window)
}

// splitFilterValues trims repeated query parameters rather than splitting on
// a separator: an Apple hardware identifier is "iPhone18,2".
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

// parsePlatformFilters returns the normalized values, not the ones that came
// in, since platform is stored lowercase.
func parsePlatformFilters(raw []string) ([]string, bool) {
	values := splitFilterValues(raw)
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

// metadataFilter parses `key:value` attribute pairs into the containment
// documents the identity cohort lookup runs on.
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

// boundedRead gives every telemetry read the same deadline, covering the
// PostgreSQL cohort lookup that precedes the ClickHouse query too.
func boundedRead(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), telemetryReadTimeout)
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

// parseDimensions is the narrowing half of a query, without its window; split
// out because the map feed bounds itself with a cursor rather than a period.
func (h *ExplorerHandler) parseDimensions(r *http.Request) (ExplorerQuery, error) {
	platforms, ok := parsePlatformFilters(r.URL.Query()["platform"])
	if !ok {
		return ExplorerQuery{}, errInvalidObservePlatform
	}
	filter, err := h.metadataFilter(r.Context(), mux.Vars(r)["APP_ID"], r.URL.Query()["attr"])
	if err != nil {
		return ExplorerQuery{}, err
	}

	// One value out of range on any dimension rejects the whole request; the
	// failure is held and checked once.
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
// the values each one takes.
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
	readContext, cancelRead := boundedRead(r)
	defer cancelRead()
	events, err := h.reader.ReadEvents(readContext, mux.Vars(r)["APP_ID"], query)
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
	readContext, cancelRead := boundedRead(r)
	defer cancelRead()
	overview, err := h.reader.ReadOverview(readContext, mux.Vars(r)["APP_ID"], query)
	if err != nil {
		log.Printf("observe: reading overview failed: %v", err)
		h.renderQueryError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, overview)
}

// GetCheckInsHandler feeds the live map with everything that checked in since
// the cursor the client sent back.
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
	readContext, cancelRead := boundedRead(r)
	defer cancelRead()
	feed, err := h.reader.ReadCheckIns(
		readContext,
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

// GetBreakdownHandler answers "who is this slow for": one metric split by one
// dimension, ranked by volume.
func (h *ExplorerHandler) GetBreakdownHandler(w http.ResponseWriter, r *http.Request) {
	base, err := h.parseBaseQuery(r, maxOverviewWindow)
	if err != nil {
		h.renderQueryError(w, err)
		return
	}
	// The leading value wins when an old link still carries a comma-separated list.
	dimension := strings.TrimSpace(strings.Split(r.URL.Query().Get("dimension"), ",")[0])
	if dimension == "" {
		handlers.RenderError(w, http.StatusBadRequest, "'dimension' is required.")
		return
	}
	if !IsBreakdownDimension(dimension) {
		handlers.RenderError(w, http.StatusBadRequest, "'dimension' is not a known Observe dimension.")
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
			Available: false,
			Metric:    metric,
			Dimension: dimension,
			Segments:  []BreakdownSegment{},
		})
		return
	}
	readContext, cancelRead := boundedRead(r)
	defer cancelRead()
	breakdown, err := h.reader.ReadBreakdown(readContext, mux.Vars(r)["APP_ID"], BreakdownQuery{
		ExplorerQuery: base,
		Metric:        metric,
		Dimension:     dimension,
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
	readContext, cancelRead := boundedRead(r)
	defer cancelRead()
	page, err := h.reader.ReadLogs(readContext, mux.Vars(r)["APP_ID"], LogsQuery{
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
