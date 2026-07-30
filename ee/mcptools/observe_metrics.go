// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"time"

	"expo-open-ota/ee/observe"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// The overview knows up to a hundred metric names; an agent reads a
	// summary, not a catalog dump.
	defaultMetricsInOverview = 12
	maxTopLocations          = 10
	defaultBreakdownSegments = 10
	maxBreakdownSegments     = 50
	defaultSeriesPoints      = 24
	maxSeriesPoints          = 60
	defaultEventsInAnswer    = 20
	maxEventsInAnswer        = 50
)

type GetObserveOverviewInput struct {
	AppId string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	ObserveFilters
	From string `json:"from,omitempty" jsonschema:"start of the window, RFC3339; defaults to 24 hours before to, up to 90 days"`
	To   string `json:"to,omitempty" jsonschema:"end of the window, RFC3339; defaults to now"`
	// MetricIds narrows the metrics reported, and is what makes the curves
	// affordable: ask for the one you are investigating.
	MetricIds     []string `json:"metricIds,omitempty" jsonschema:"only report these metrics, by their id as returned in a previous answer"`
	IncludeSeries bool     `json:"includeSeries,omitempty" jsonschema:"add the median-over-time curve of each reported metric; only allowed together with metricIds"`
	MaxPoints     int      `json:"maxPoints,omitempty" jsonschema:"points per curve when includeSeries is set; default 24, max 60"`
}

type ObserveSummaryOutput struct {
	Users     uint64   `json:"users" jsonschema:"distinct installs that reported anything in the window"`
	Sessions  uint64   `json:"sessions"`
	Events    uint64   `json:"events" jsonschema:"log lines received"`
	Releases  uint64   `json:"releases" jsonschema:"distinct app versions"`
	Builds    uint64   `json:"builds"`
	Updates   uint64   `json:"updates" jsonschema:"distinct OTA updates seen running, the embedded bundle excluded"`
	Platforms []string `json:"platforms,omitempty"`
}

type MetricOutput struct {
	Id       string  `json:"id" jsonschema:"pass it back as metricIds, or as metric to get_metric_breakdown"`
	Name     string  `json:"name" jsonschema:"the raw metric name the SDK reports"`
	Label    string  `json:"label"`
	Unit     string  `json:"unit"`
	Category string  `json:"category"`
	Samples  uint64  `json:"samples"`
	Devices  uint64  `json:"devices"`
	Median   float64 `json:"median"`
	P90      float64 `json:"p90"`
	P99      float64 `json:"p99"`
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	// Series is set only when includeSeries was asked for.
	Series []MetricPointOutput `json:"series,omitempty" jsonschema:"median per bucket, oldest first"`
}

type MetricPointOutput struct {
	Timestamp string  `json:"timestamp"`
	Value     float64 `json:"value"`
}

type LocationOutput struct {
	CountryCode string `json:"countryCode"`
	City        string `json:"city,omitempty"`
	DeviceCount uint64 `json:"deviceCount"`
}

type GetObserveOverviewOutput struct {
	Available bool                 `json:"available" jsonschema:"false when this deployment has no ClickHouse: only users and locations are known, every metric is absent"`
	Summary   ObserveSummaryOutput `json:"summary"`
	Metrics   []MetricOutput       `json:"metrics,omitempty" jsonschema:"the busiest metrics of the window, with their distribution"`
	Locations []LocationOutput     `json:"locations,omitempty" jsonschema:"the top cities by device count"`
	Note      string               `json:"note,omitempty"`
}

func getObserveOverviewHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetObserveOverviewInput) (*mcpprot.CallToolResult, GetObserveOverviewOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetObserveOverviewInput) (*mcpprot.CallToolResult, GetObserveOverviewOutput, error) {
		if err := deps.requireTelemetry(ctx, req, input.AppId); err != nil {
			return nil, GetObserveOverviewOutput{}, err
		}
		if input.IncludeSeries && len(input.MetricIds) == 0 {
			return nil, GetObserveOverviewOutput{}, errors.New("includeSeries needs metricIds: name the metrics you want curves for")
		}
		query, err := deps.explorerQuery(ctx, input.AppId, input.ObserveFilters, input.From, input.To, maxObserveWindow)
		if err != nil {
			return nil, GetObserveOverviewOutput{}, err
		}
		ctx, cancel := boundedRead(ctx)
		defer cancel()
		overview, err := deps.Explorer.ReadOverview(ctx, input.AppId, query)
		if err != nil {
			log.Printf("mcp get_observe_overview failed for app %s: %v", input.AppId, err)
			return nil, GetObserveOverviewOutput{}, errors.New("could not read the overview, try again later")
		}

		output := GetObserveOverviewOutput{
			Available: overview.Available,
			Summary: ObserveSummaryOutput{
				Users:     overview.Summary.Users,
				Sessions:  overview.Summary.Sessions,
				Events:    overview.Summary.Events,
				Releases:  overview.Summary.Releases,
				Builds:    overview.Summary.Builds,
				Updates:   overview.Summary.Updates,
				Platforms: overview.Summary.Platforms,
			},
		}
		if !overview.Available {
			output.Note = "this deployment has no ClickHouse configured, so timings, events and logs are not stored; the device registry still answers"
		}

		wanted := map[string]bool{}
		for _, id := range input.MetricIds {
			wanted[id] = true
		}
		// The service returns the metrics in catalog order; trimming that to a
		// dozen would drop whatever reports last in the catalog rather than
		// what reports least.
		reported := append([]observe.MetricSeries(nil), overview.Metrics...)
		slices.SortStableFunc(reported, func(left, right observe.MetricSeries) int {
			switch {
			case left.Stats.Count > right.Stats.Count:
				return -1
			case left.Stats.Count < right.Stats.Count:
				return 1
			default:
				return 0
			}
		})
		points := input.MaxPoints
		if points <= 0 {
			points = defaultSeriesPoints
		}
		if points > maxSeriesPoints {
			points = maxSeriesPoints
		}
		for _, metric := range reported {
			if len(wanted) > 0 && !wanted[metric.ID] {
				continue
			}
			if len(wanted) == 0 && len(output.Metrics) >= defaultMetricsInOverview {
				continue
			}
			answer := MetricOutput{
				Id:       metric.ID,
				Name:     metric.Name,
				Label:    metric.Label,
				Unit:     metric.Unit,
				Category: metric.Category,
				Samples:  metric.Stats.Count,
				Devices:  metric.Stats.Devices,
				Median:   metric.Stats.Median,
				P90:      metric.Stats.P90,
				P99:      metric.Stats.P99,
				Min:      metric.Stats.Min,
				Max:      metric.Stats.Max,
			}
			if input.IncludeSeries {
				answer.Series = resampleMetricPoints(metric.Points, points)
			}
			output.Metrics = append(output.Metrics, answer)
		}
		if len(wanted) > 0 && len(output.Metrics) == 0 && overview.Available {
			return nil, GetObserveOverviewOutput{}, errors.New("no metric in this window matches the metricIds asked for; call this tool without metricIds to see what reported")
		}

		for _, location := range overview.Locations {
			if len(output.Locations) >= maxTopLocations {
				break
			}
			output.Locations = append(output.Locations, LocationOutput{
				CountryCode: location.CountryCode,
				City:        location.City,
				DeviceCount: location.DeviceCount,
			})
		}
		return nil, output, nil
	}
}

func resampleMetricPoints(points []observe.ObserveMetricPoint, max int) []MetricPointOutput {
	if len(points) == 0 {
		return nil
	}
	kept := make([]MetricPointOutput, 0, min(len(points), max))
	for _, index := range resampleIndexes(len(points), max) {
		kept = append(kept, MetricPointOutput{
			Timestamp: points[index].Timestamp.UTC().Format(time.RFC3339),
			Value:     points[index].Value,
		})
	}
	return kept
}

func registerGetObserveOverview(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name: "get_observe_overview",
		Description: "What an app's fleet did over a window: how many installs and sessions reported, which releases and updates they ran, where they are, and the distribution of every timing the SDK measured (median, p90, p99, sample and device counts). " +
			"Start here, then pass a metric id to get_metric_breakdown to find what makes it slow. Requires the observe:read permission.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Telemetry overview", ReadOnlyHint: true},
	}, getObserveOverviewHandler(deps))
}

type GetMetricBreakdownInput struct {
	AppId string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	ObserveFilters
	Metric     string `json:"metric" jsonschema:"the metric id, as returned by get_observe_overview; only the built-in timings can be split, not custom metrics"`
	Dimension  string `json:"dimension" jsonschema:"what to split by: deviceModel, osVersion, osName, country, appVersion, appBuildNumber, route, update, updateGroup, branch, runtimeVersion, channel, environment, platform, thermalState, networkType, lowPowerMode, frozenFrames or networkBytes"`
	From       string `json:"from,omitempty" jsonschema:"start of the window, RFC3339; defaults to 24 hours before to, up to 90 days"`
	To         string `json:"to,omitempty" jsonschema:"end of the window, RFC3339; defaults to now"`
	Limit      int    `json:"limit,omitempty" jsonschema:"segments returned; default 10, max 50"`
	WithSeries bool   `json:"withSeries,omitempty" jsonschema:"add the median-over-time curve of the segments that stand out most; the server plots at most 8"`
	MaxPoints  int    `json:"maxPoints,omitempty" jsonschema:"points per curve when withSeries is set; default 24, max 60"`
}

type BreakdownSegmentOutput struct {
	Value   string  `json:"value"`
	Context string  `json:"context,omitempty" jsonschema:"disambiguates the value, e.g. the OS name behind an OS version"`
	Devices uint64  `json:"devices"`
	Samples uint64  `json:"samples"`
	P50     float64 `json:"p50"`
	P90     float64 `json:"p90"`
	// Series is set only for the segments the server considered worth plotting.
	Series []MetricPointOutput `json:"series,omitempty"`
}

type GetMetricBreakdownOutput struct {
	Available bool                     `json:"available" jsonschema:"false when this deployment has no ClickHouse"`
	Metric    string                   `json:"metric"`
	Dimension string                   `json:"dimension"`
	Overall   BreakdownSegmentOutput   `json:"overall" jsonschema:"the same statistics over every segment, the baseline to compare against"`
	Segments  []BreakdownSegmentOutput `json:"segments" jsonschema:"slowest-first is not guaranteed; compare each p50 against overall"`
	Note      string                   `json:"note,omitempty"`
}

func getMetricBreakdownHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetMetricBreakdownInput) (*mcpprot.CallToolResult, GetMetricBreakdownOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetMetricBreakdownInput) (*mcpprot.CallToolResult, GetMetricBreakdownOutput, error) {
		if err := deps.requireTelemetry(ctx, req, input.AppId); err != nil {
			return nil, GetMetricBreakdownOutput{}, err
		}
		metric := strings.TrimSpace(input.Metric)
		if metric == "" {
			return nil, GetMetricBreakdownOutput{}, errors.New("metric is required; get_observe_overview returns the ids")
		}
		// A breakdown only knows the built-in timings: a custom metric reports
		// in the overview but cannot be split, and saying so beats an opaque
		// refusal from the query builder.
		known := make([]string, 0, len(observe.MetricDefinitions()))
		splittable := false
		for _, definition := range observe.MetricDefinitions() {
			known = append(known, definition.ID)
			if definition.ID == metric {
				splittable = true
			}
		}
		if !splittable {
			return nil, GetMetricBreakdownOutput{}, errors.New("only the built-in timings can be split; metric must be one of: " + strings.Join(known, ", "))
		}
		dimension := strings.TrimSpace(input.Dimension)
		if !observe.IsBreakdownDimension(dimension) {
			return nil, GetMetricBreakdownOutput{}, errors.New("dimension must be one of: " + strings.Join(observe.BreakdownDimensions(), ", "))
		}
		query, err := deps.explorerQuery(ctx, input.AppId, input.ObserveFilters, input.From, input.To, maxObserveWindow)
		if err != nil {
			return nil, GetMetricBreakdownOutput{}, err
		}
		limit := input.Limit
		if limit <= 0 {
			limit = defaultBreakdownSegments
		}
		if limit > maxBreakdownSegments {
			limit = maxBreakdownSegments
		}
		points := input.MaxPoints
		if points <= 0 {
			points = defaultSeriesPoints
		}
		if points > maxSeriesPoints {
			points = maxSeriesPoints
		}

		ctx, cancel := boundedRead(ctx)
		defer cancel()
		breakdown, err := deps.Explorer.ReadBreakdown(ctx, input.AppId, observe.BreakdownQuery{
			ExplorerQuery: query,
			Metric:        metric,
			Dimension:     dimension,
			Limit:         limit,
			WithPoints:    input.WithSeries,
		})
		if err != nil {
			log.Printf("mcp get_metric_breakdown failed for app %s: %v", input.AppId, err)
			return nil, GetMetricBreakdownOutput{}, errors.New("could not read the breakdown, try again later")
		}

		output := GetMetricBreakdownOutput{
			Available: breakdown.Available,
			Metric:    breakdown.Metric,
			Dimension: breakdown.Dimension,
			Overall:   breakdownSegment(breakdown.Overall, points),
			Segments:  make([]BreakdownSegmentOutput, 0, len(breakdown.Segments)),
		}
		if !breakdown.Available {
			output.Note = "this deployment has no ClickHouse configured, so timings are not stored"
		}
		for _, segment := range breakdown.Segments {
			output.Segments = append(output.Segments, breakdownSegment(segment, points))
		}
		return nil, output, nil
	}
}

func breakdownSegment(segment observe.BreakdownSegment, points int) BreakdownSegmentOutput {
	return BreakdownSegmentOutput{
		Value:   segment.Value,
		Context: segment.Context,
		Devices: segment.Devices,
		Samples: segment.Samples,
		P50:     segment.P50,
		P90:     segment.P90,
		Series:  resampleMetricPoints(segment.Points, points),
	}
}

func registerGetMetricBreakdown(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name: "get_metric_breakdown",
		Description: "Split one timing by one dimension to find what makes it slow: per segment, how many devices and samples it holds and its p50 and p90, plus the same over the whole population to compare against. " +
			"This is the deep dive behind get_observe_overview. Requires the observe:read permission.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Metric breakdown", ReadOnlyHint: true},
	}, getMetricBreakdownHandler(deps))
}

type GetObserveEventsInput struct {
	AppId string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	ObserveFilters
	From  string `json:"from,omitempty" jsonschema:"start of the window, RFC3339; defaults to 24 hours before to, up to 90 days"`
	To    string `json:"to,omitempty" jsonschema:"end of the window, RFC3339; defaults to now"`
	Limit int    `json:"limit,omitempty" jsonschema:"event names returned, busiest first; default 20, max 50"`
}

type EventSeriesOutput struct {
	Name     string `json:"name" jsonschema:"pass it to query_logs as eventNames to read the lines"`
	Count    uint64 `json:"count"`
	Users    uint64 `json:"users"`
	Sessions uint64 `json:"sessions"`
}

type GetObserveEventsOutput struct {
	Available bool                `json:"available" jsonschema:"false when this deployment has no ClickHouse"`
	Events    []EventSeriesOutput `json:"events" jsonschema:"busiest first"`
	Note      string              `json:"note,omitempty"`
}

func getObserveEventsHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetObserveEventsInput) (*mcpprot.CallToolResult, GetObserveEventsOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetObserveEventsInput) (*mcpprot.CallToolResult, GetObserveEventsOutput, error) {
		if err := deps.requireTelemetry(ctx, req, input.AppId); err != nil {
			return nil, GetObserveEventsOutput{}, err
		}
		if err := input.ObserveFilters.rejectConditions(); err != nil {
			return nil, GetObserveEventsOutput{}, err
		}
		query, err := deps.explorerQuery(ctx, input.AppId, input.ObserveFilters, input.From, input.To, maxObserveWindow)
		if err != nil {
			return nil, GetObserveEventsOutput{}, err
		}
		limit := input.Limit
		if limit <= 0 {
			limit = defaultEventsInAnswer
		}
		if limit > maxEventsInAnswer {
			limit = maxEventsInAnswer
		}
		ctx, cancel := boundedRead(ctx)
		defer cancel()
		events, err := deps.Explorer.ReadEvents(ctx, input.AppId, query)
		if err != nil {
			log.Printf("mcp get_observe_events failed for app %s: %v", input.AppId, err)
			return nil, GetObserveEventsOutput{}, errors.New("could not read the events, try again later")
		}

		output := GetObserveEventsOutput{Available: events.Available, Events: make([]EventSeriesOutput, 0, min(len(events.Events), limit))}
		if !events.Available {
			output.Note = "this deployment has no ClickHouse configured, so events are not stored"
		}
		for _, event := range events.Events {
			if len(output.Events) >= limit {
				break
			}
			output.Events = append(output.Events, EventSeriesOutput{
				Name:     event.Name,
				Count:    event.Count,
				Users:    event.Users,
				Sessions: event.Sessions,
			})
		}
		slices.SortStableFunc(output.Events, func(left, right EventSeriesOutput) int {
			return int(right.Count) - int(left.Count)
		})
		return nil, output, nil
	}
}

func registerGetObserveEvents(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_observe_events",
		Description: "Which events an app emitted over a window, busiest first, with how many lines, installs and sessions each covers. Use it to discover event names before reading their lines with query_logs. Requires the observe:read permission.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Telemetry events", ReadOnlyHint: true},
	}, getObserveEventsHandler(deps))
}
