// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

// A breakdown answers "who is this slow for". One metric, one or more
// dimensions, one row per segment with the percentiles that matter and the
// number of devices behind them, so a segment can be ranked by how many people
// it hurts rather than alphabetically.
type BreakdownSegment struct {
	// Value is the raw column value this segment groups on. It doubles as the
	// filter to apply when drilling into the segment, which is why it stays raw
	// instead of being a display label the dashboard would have to parse back.
	Value string `json:"value"`
	// Context qualifies the value when it means nothing alone: an OS version of
	// "18.6" is only readable next to its OS name. Empty for every dimension
	// that needs no qualifier.
	Context string  `json:"context,omitempty"`
	Devices uint64  `json:"devices"`
	Samples uint64  `json:"samples"`
	P50     float64 `json:"p50"`
	P90     float64 `json:"p90"`
	// Points is the median over time for this segment, present only when the
	// caller asked for series: it costs a second query, and the ranking alone
	// is what most callers want.
	Points []ObserveMetricPoint `json:"points,omitempty"`
}

type Breakdown struct {
	Available bool               `json:"available"`
	Metric    string             `json:"metric"`
	Dimension string             `json:"dimension"`
	Segments  []BreakdownSegment `json:"segments"`
	// Overall is the same metric over the same filters with no grouping, so
	// each segment can be read as a deviation from the app's own baseline
	// instead of an absolute number nobody has a feel for.
	Overall BreakdownSegment `json:"overall"`
}

type BreakdownQuery struct {
	ExplorerQuery
	// Metric is a definition ID from observedMetricDefinitions, resolved to a
	// metric_name before it ever reaches SQL.
	Metric string
	// Dimension is the single column the segments group on. Empty is rejected.
	//
	// One, deliberately not a list: two dimensions at once multiply into a
	// chart that cannot be read, and they answer a question that is better
	// asked as two. Narrow to one device model with a filter, then split by OS
	// version.
	Dimension string
	Limit     int
	// WithPoints fills Segment.Points with a median-per-bucket series.
	WithPoints bool
}

// paramString reads one key of the params the client attached to a data point.
// They arrive as a single JSON string with flat, dotted key names, so a key is
// an argument rather than a path.
func paramString(key string) sqlFragment {
	return sqlFragment(fmt.Sprintf("JSONExtractString(m.custom_params, '%s')", key))
}

// paramBool keeps a boolean readable: grouped raw it would rank a segment
// labelled "true" against one labelled "false".
func paramBool(key, whenTrue, whenFalse string) sqlFragment {
	return sqlFragment(fmt.Sprintf(
		"if(JSONHas(m.custom_params, '%s'), if(JSONExtractBool(m.custom_params, '%s'), '%s', '%s'), '')",
		key, key, whenTrue, whenFalse,
	))
}

// paramBuckets turns a numeric param into named ranges. A continuous value has
// about as many distinct values as there are samples, so grouping on it raw
// produces one segment per device and ranks nothing. labels carries one more
// entry than bounds: the open-ended range above the last one.
//
// The labels are also the values a filter on this dimension takes, which is why
// they are declared once here and served to the dashboard rather than restated
// there: a bucket renamed on this side would otherwise leave a picker offering
// a range the query can no longer match.
func paramBuckets(key string, bounds []float64, labels []string) sqlFragment {
	value := fmt.Sprintf("JSONExtractFloat(m.custom_params, '%s')", key)
	expression := fmt.Sprintf("multiIf(NOT JSONHas(m.custom_params, '%s'), ''", key)
	for i, bound := range bounds {
		expression += fmt.Sprintf(
			", %s <= %s, '%s'",
			value, strconv.FormatFloat(bound, 'f', -1, 64), labels[i],
		)
	}
	return sqlFragment(expression + fmt.Sprintf(", '%s')", labels[len(bounds)]))
}

// Dimension name to the columns it groups on. An allowlist, not a mapping
// helper: the value lands in the SQL string itself, so anything outside this
// table must never reach the query builder.
var breakdownDimensions = map[string]struct {
	column  sqlFragment
	context sqlFragment
	// expr replaces the column for a dimension that is not one: the conditions
	// a timing was measured under travel inside a single JSON string, and only
	// the data points whose client attached params carry them at all.
	expr sqlFragment
	// values is what a filter on this dimension accepts, in the order a picker
	// should offer them. Set on the conditions only: every other dimension
	// takes whatever the fleet reported, which no list here could enumerate.
	values []string
	// session says the value belongs to the session rather than to the row, so
	// reading it costs a join and every timing can answer for it.
	session bool
}{
	"deviceModel": {column: "device_model"},
	// expo-router integration only: route_name is empty on app-wide timings,
	// which is exactly what makes "which screen is slow" answerable.
	"route":          {column: "route_name"},
	"osVersion":      {column: "os_version", context: "os_name"},
	"osName":         {column: "os_name"},
	"country":        {column: "country_code"},
	"appVersion":     {column: "app_version"},
	"appBuildNumber": {column: "app_build_number"},
	"update":         {column: "update_id"},
	// The publish, not the per-platform row: "my last release" is a group of
	// one or two updates, and grouping by update splits it in two.
	"updateGroup":    {column: "update_group_id"},
	"branch":         {column: "branch"},
	"runtimeVersion": {column: "runtime_version"},
	"channel":        {column: "channel"},
	"environment":    {column: "environment"},
	"platform":       {column: "platform"},
	// The conditions the device was in when it timed itself. They answer the
	// question a percentile alone never does: a p90 of four seconds is a
	// complaint, the same p90 next to "thermally throttled" is a diagnosis.
	// Attached by the framework to the interactive timings only.
	"thermalState": {expr: paramString("expo.device.thermalState"), values: thermalStates},
	// Read off the session rather than off the row. The client attaches the
	// conditions to the interactive timing alone, so an update download would
	// never know whether it ran on wifi, which is the first thing anyone asks
	// about a download. What the phone was connected to does not change between
	// two timings of the same session, so borrowing it is sound; the counters
	// below are tallied for one measurement and borrowing those would not be.
	"networkType": {expr: sessionNetwork, values: networkTypes, session: true},
	"lowPowerMode": {
		expr:   paramBool("expo.device.lowPowerMode", lowPowerOn, lowPowerOff),
		values: powerModes,
	},
	"frozenFrames": {
		expr:   paramBuckets("expo.frameRate.frozenFrames", []float64{0, 2, 8, 20}, frozenFrames),
		values: frozenFrames,
	},
	"networkBytes": {
		expr: paramBuckets(
			"expo.network.requests.bytesReceived",
			[]float64{100_000, 500_000, 2_000_000},
			networkBytes,
		),
		values: networkBytes,
	},
}

const (
	lowPowerOn  = "Low power mode"
	lowPowerOff = "Normal power"
	// The one metric the client attaches the conditions to, verified on both
	// platforms: MetricParamsBuilder has a single call site, inside
	// markInteractive. Everything session-scoped is read from it.
	interactiveMetric = "expo.app_startup.tti"
	// The alias the join below is given, and how a session-scoped dimension
	// spells itself in the query.
	sessionNetwork sqlFragment = "session_state.network"
)

// A timing read that needs a session-scoped condition joins the session's own
// interactive timing to get it. LEFT, so a session that never became
// interactive still contributes its rows: they land with an empty network and
// the split drops them, which beats dropping the whole session silently.
func metricsSource(appID string, query ExplorerQuery, dimension string) (sqlFragment, []any) {
	scoped := breakdownDimensions[dimension].session
	for name, values := range query.Conditions {
		scoped = scoped || (len(values) > 0 && breakdownDimensions[name].session)
	}
	if !scoped {
		return "observe_metrics m", nil
	}
	return sqlFragment(sqlf(`observe_metrics m
		LEFT JOIN (
			SELECT session_id,
			       JSONExtractString(any(custom_params), 'expo.network.type') AS network
			FROM observe_metrics
			WHERE app_id = ? AND timestamp >= ? AND timestamp <= ?
			  AND metric_name = '%s'
			  AND JSONHas(custom_params, 'expo.network.type')
			GROUP BY session_id
		) session_state USING (session_id)`, interactiveMetric)),
		[]any{appID, query.From.UTC(), query.To.UTC()}
}

// What each condition can be, worst last so a picker reads as a scale rather
// than as an alphabetical list. The two the device names itself are the client
// enums; the rest are ranges this file chose, which is why it also serves them.
var (
	thermalStates = []string{"nominal", "fair", "serious", "critical"}
	networkTypes  = []string{"wifi", "cellular", "none"}
	powerModes    = []string{lowPowerOff, lowPowerOn}
	frozenFrames  = []string{"None", "1 to 2", "3 to 8", "9 to 20", "Over 20"}
	networkBytes  = []string{"Under 100 kB", "100 to 500 kB", "500 kB to 2 MB", "Over 2 MB"}
)

// ConditionDefinition is one filterable condition and the values it takes. The
// dashboard renders a picker per entry, so this is what keeps the ranges it
// offers and the ranges the SQL produces the same list.
type ConditionDefinition struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
	// SessionScoped says the condition is read off the session rather than off
	// the measurement, so every timing can be split and filtered by it. The
	// dashboard needs it to know which cards to keep on screen.
	SessionScoped bool `json:"sessionScoped"`
}

func ObserveConditions() []ConditionDefinition {
	names := ConditionDimensions()
	definitions := make([]ConditionDefinition, 0, len(names))
	for _, name := range names {
		definitions = append(definitions, ConditionDefinition{
			Name:          name,
			Values:        breakdownDimensions[name].values,
			SessionScoped: breakdownDimensions[name].session,
		})
	}
	return definitions
}

func IsBreakdownDimension(name string) bool {
	_, found := breakdownDimensions[name]
	return found
}

// ConditionDimensions names the dimensions that read a measurement's own params
// rather than a column, in a stable order. The handler walks it to know which
// query parameters to accept, so the allowlist below stays the single place a
// condition is declared.
func ConditionDimensions() []string {
	names := make([]string, 0, len(breakdownDimensions))
	for name, dimension := range breakdownDimensions {
		if dimension.expr != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// conditionsWhere narrows to the measurements taken in a given state. Applied
// on the timing reads only, and never on the summary, the logs or the device
// registry: none of them holds the params this reads, so a filter they cannot
// honor has to be refused rather than silently ignored.
func conditionsWhere(conditions map[string][]string) (sqlFragment, []any) {
	if len(conditions) == 0 {
		return "", nil
	}
	var where sqlFragment
	var args []any
	rowScoped := false
	// Sorted, not map order: the same filters must produce the same SQL every
	// time or ClickHouse caches a plan per permutation.
	for _, name := range ConditionDimensions() {
		values := conditions[name]
		if len(values) == 0 {
			continue
		}
		rowScoped = rowScoped || !breakdownDimensions[name].session
		where += " AND " + breakdownDimensions[name].expr + " IN ?"
		args = append(args, values)
	}
	if args == nil {
		return "", nil
	}
	if rowScoped {
		// A string comparison before any JSON is parsed. Two rows in three carry
		// no params at all, and none of them can match a condition read off the
		// row, so this drops them for the price of a byte. Never for a
		// session-scoped one: there the row is meant to have no params of its
		// own, and this would discard exactly what the join went to fetch.
		where = " AND m.custom_params != ''" + where
	}
	return where, args
}

func metricNameForID(id string) (string, bool) {
	for _, definition := range observedMetricDefinitions {
		if definition.ID == id {
			return definition.Name, true
		}
	}
	return "", false
}

const (
	maxBreakdownSegments = 50
	// Overlaying more than this many series turns a chart into a hairball, so
	// the points query stops there even when more segments are ranked.
	maxBreakdownSeries = 8
	// Below this, a segment's median is noise rather than a measurement: one
	// unlucky device would otherwise out-rank a regression hitting hundreds.
	// The dashboard applies the same floor, so both agree on what is worth
	// looking at.
	minDevicesToRank = 5
)

// plottableSegments picks the series worth drawing: the ones the dashboard
// ranks highest, not the ones with the most samples. Ranking by volume here
// and by impact there would draw one set of curves and list another, leaving
// rows whose colour matches no line on the chart.
func plottableSegments(segments []BreakdownSegment, baselineP50 float64) []BreakdownSegment {
	ranked := make([]BreakdownSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.Devices >= minDevicesToRank {
			ranked = append(ranked, segment)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		left := float64(ranked[i].Devices) * math.Max(0, ranked[i].P50-baselineP50)
		right := float64(ranked[j].Devices) * math.Max(0, ranked[j].P50-baselineP50)
		if left != right {
			return left > right
		}
		return ranked[i].Devices > ranked[j].Devices
	})
	if len(ranked) > maxBreakdownSeries {
		ranked = ranked[:maxBreakdownSeries]
	}
	return ranked
}

// breakdownGrouping is the pair of columns a dimension groups by: the value
// itself, and the qualifier that makes it readable on its own (” when the
// value needs none). Kept together because every use reads them in lockstep.
type breakdownGrouping struct {
	column  sqlFragment
	context sqlFragment
}

// groupingSQL builds the aliases shared by both queries below, so the scan
// order never has to be inferred from the underlying columns. `selected` is
// wrapped in any() because the inner query collapses duplicate data points
// rather than grouping on the dimension itself: every row behind one
// deduplication key carries the same value, so any() of them is that value.
func groupingSQL(grouping breakdownGrouping) (selected, aliases []sqlFragment) {
	return []sqlFragment{
			sqlFragment(sqlf("any(%s) AS s0", grouping.column)),
			sqlFragment(sqlf("any(%s) AS c0", grouping.context)),
		},
		[]sqlFragment{"s0", "c0"}
}

// dedupKey is the deduplication the rest of the explorer uses, as one key: a
// published client re-sends an entire batch after any failed dispatch, so the
// same data point can land twice. Grouping on one hash rather than on the six
// columns it stands for is what keeps that collapse from costing more than the
// aggregate it feeds.
const dedupKey sqlFragment = `if(m.content_hash = 0,
	            cityHash64(m.eas_client_id, m.session_id, m.metric_name, m.timestamp, m.value),
	            m.content_hash)`

// The inner GROUP BY is the deduplication the rest of the explorer uses: a
// published client re-sends an entire batch after any failed dispatch, so the
// same data point can land twice and content_hash is what collapses it.
func (e *Explorer) ReadBreakdown(
	ctx context.Context,
	appID string,
	query BreakdownQuery,
) (Breakdown, error) {
	dimension, found := breakdownDimensions[query.Dimension]
	if query.Dimension == "" || !found {
		return Breakdown{}, errInvalidObserveFilter
	}
	var grouping breakdownGrouping
	// Set when the dimension only exists where the client attached params, so
	// the read can be narrowed to those data points below.
	var condition sqlFragment
	rowScopedSplit := false
	if dimension.expr != "" {
		grouping.column = dimension.expr
		condition = dimension.expr
		rowScopedSplit = !dimension.session
	} else {
		grouping.column = "m." + dimension.column
	}
	grouping.context = "''"
	if dimension.context != "" {
		grouping.context = "m." + dimension.context
	}
	metricName, found := metricNameForID(query.Metric)
	if !found {
		return Breakdown{}, errInvalidObserveFilter
	}
	limit := query.Limit
	if limit <= 0 || limit > maxBreakdownSegments {
		limit = maxBreakdownSegments
	}

	breakdown := Breakdown{
		Available: e.clickhouse != nil,
		Metric:    query.Metric,
		Dimension: query.Dimension,
		Segments:  []BreakdownSegment{},
	}
	if e.clickhouse == nil {
		return breakdown, nil
	}
	queryContext, resolved, empty, err := e.prepareTelemetryRead(ctx, appID, query.ExplorerQuery)
	if err != nil {
		return Breakdown{}, err
	}
	query.ExplorerQuery = resolved
	if empty {
		return breakdown, nil
	}
	cohort := len(query.MetadataFilter) > 0
	where, args := telemetryWhere("m", query.ExplorerQuery, cohort)
	conditionWhere, conditionArgs := conditionsWhere(query.Conditions)
	where += conditionWhere
	args = append(args, conditionArgs...)
	// Splitting on a condition drops the samples that never reported one. They
	// would otherwise collapse into a single nameless segment out-ranking every
	// real one, and drag down the baseline the others are compared against: the
	// point of the split is to compare devices that were throttled with devices
	// that were not, and a sample that says neither belongs to no side.
	if rowScopedSplit {
		// Cheap first, for the same reason as conditionsWhere, and for the same
		// reason skipped when the split reads the session instead of the row.
		where += " AND m.custom_params != ''"
	}
	if condition != "" {
		where += " AND " + condition + " != ''"
	}
	source, sourceArgs := metricsSource(appID, query.ExplorerQuery, query.Dimension)
	selected, aliases := groupingSQL(grouping)

	// uniq, not uniqExact: this is the "N devices" of a table cell, and an exact
	// distinct count over millions of rows costs more than every percentile on
	// the row put together, for a figure nobody reconciles to the unit.
	sql := sqlf(`
		SELECT %s, uniq(eas_client_id), count(),
		       toFloat64(quantileTDigest(0.5)(value)),
		       toFloat64(quantileTDigest(0.9)(value))
		FROM (
			SELECT %s, any(m.eas_client_id) AS eas_client_id, any(m.value) AS value
			FROM %s
			WHERE %s AND m.metric_name = ?
			GROUP BY %s
		)
		GROUP BY %s
		ORDER BY count() DESC, %s
		LIMIT ?`,
		joinFragments(aliases, ", "),
		joinFragments(selected, ", "),
		source,
		where,
		dedupKey,
		joinFragments(aliases, ", "),
		joinFragments(aliases, ", "),
	)

	segmentArgs := append(append(sourceArgs[:len(sourceArgs):len(sourceArgs)],
		prependAppID(appID, args)...), metricName, uint64(limit))
	rows, err := e.clickhouse.Conn.Query(queryContext, sql, segmentArgs...)
	if err != nil {
		return Breakdown{}, fmt.Errorf("reading observe breakdown: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var segment BreakdownSegment
		if err := rows.Scan(&segment.Value, &segment.Context,
			&segment.Devices, &segment.Samples, &segment.P50, &segment.P90); err != nil {
			return Breakdown{}, err
		}
		segment.P50, segment.P90 = finite(segment.P50), finite(segment.P90)
		breakdown.Segments = append(breakdown.Segments, segment)
	}
	if err := rows.Err(); err != nil {
		return Breakdown{}, err
	}

	// Percentiles cannot be averaged back together from the segments, so the
	// baseline is its own query rather than arithmetic on the rows above.
	overallSQL := sqlf(`
		SELECT uniq(eas_client_id), count(),
		       toFloat64(quantileTDigest(0.5)(value)),
		       toFloat64(quantileTDigest(0.9)(value))
		FROM (
			SELECT any(m.eas_client_id) AS eas_client_id, any(m.value) AS value
			FROM %s
			WHERE %s AND m.metric_name = ?
			GROUP BY %s
		)`, source, where, dedupKey)
	overallArgs := append(append(sourceArgs[:len(sourceArgs):len(sourceArgs)],
		prependAppID(appID, args)...), metricName)
	if err := e.clickhouse.Conn.QueryRow(queryContext, overallSQL, overallArgs...).Scan(
		&breakdown.Overall.Devices, &breakdown.Overall.Samples,
		&breakdown.Overall.P50, &breakdown.Overall.P90,
	); err != nil {
		return Breakdown{}, fmt.Errorf("reading observe breakdown baseline: %w", err)
	}
	breakdown.Overall.P50 = finite(breakdown.Overall.P50)
	breakdown.Overall.P90 = finite(breakdown.Overall.P90)

	if query.WithPoints && len(breakdown.Segments) > 0 {
		if err := e.readBreakdownPoints(
			queryContext, appID, query, grouping, source, sourceArgs, where, args, metricName,
			breakdown.Segments, breakdown.Overall.P50,
		); err != nil {
			return Breakdown{}, err
		}
	}
	return breakdown, nil
}

// readBreakdownPoints fills the median-per-bucket series of the top segments,
// restricted to the ones already ranked: pulling every segment's series would
// return a long tail nobody plots.
func (e *Explorer) readBreakdownPoints(
	ctx context.Context,
	appID string,
	query BreakdownQuery,
	grouping breakdownGrouping,
	source sqlFragment,
	sourceArgs []any,
	where sqlFragment,
	whereArgs []any,
	metricName string,
	// segments is the set being filled in place; the ranking that decides which
	// of them get a series is done here rather than by the caller, so the two
	// cannot be handed over in the wrong order.
	segments []BreakdownSegment,
	baselineP50 float64,
) error {
	plotted := plottableSegments(segments, baselineP50)
	if len(plotted) == 0 {
		return nil
	}

	// The value and its context travel as a pair: an OS version alone matches
	// the same string under another OS name.
	tupleColumns := []sqlFragment{grouping.column, grouping.context}
	placeholders := make([]sqlFragment, 0, len(plotted))
	tupleArgs := make([]any, 0, len(plotted)*2)
	for _, segment := range plotted {
		placeholders = append(placeholders, "(?,?)")
		tupleArgs = append(tupleArgs, segment.Value, segment.Context)
	}
	selected, aliases := groupingSQL(grouping)

	sql := sqlf(`
		SELECT %s, toStartOfInterval(timestamp, toIntervalSecond(?)) AS bucket,
		       toFloat64(quantileTDigest(0.5)(value))
		FROM (
			SELECT %s, any(m.timestamp) AS timestamp, any(m.value) AS value
			FROM %s
			WHERE %s AND m.metric_name = ? AND (%s) IN (%s)
			GROUP BY %s
		)
		GROUP BY %s, bucket
		ORDER BY bucket`,
		joinFragments(aliases, ", "),
		joinFragments(selected, ", "),
		source,
		where,
		joinFragments(tupleColumns, ", "),
		joinFragments(placeholders, ", "),
		dedupKey,
		joinFragments(aliases, ", "),
	)

	args := []any{uint64(max(int64(query.Bucket/time.Second), 1))}
	args = append(args, sourceArgs...)
	args = append(args, prependAppID(appID, whereArgs)...)
	args = append(args, metricName)
	args = append(args, tupleArgs...)

	rows, err := e.clickhouse.Conn.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("reading observe breakdown points: %w", err)
	}
	defer rows.Close()

	index := make(map[string]int, len(segments))
	for i := range segments {
		index[segmentKey(segments[i].Value, segments[i].Context)] = i
	}
	for rows.Next() {
		var value, context string
		var point ObserveMetricPoint
		if err := rows.Scan(&value, &context, &point.Timestamp, &point.Value); err != nil {
			return err
		}
		point.Value = finite(point.Value)
		if i, found := index[segmentKey(value, context)]; found {
			segments[i].Points = append(segments[i].Points, point)
		}
	}
	return rows.Err()
}

// finite replaces the percentile of an empty set with zero. ClickHouse answers
// nan when there is nothing to take a quantile of, which JSON has no way to
// spell: marshalling fails and the whole response becomes a 500. Splitting a
// timing that reports no conditions asks exactly that question, and the honest
// answer to it is an empty breakdown, not an error.
func finite(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

// segmentKey joins a segment's value and context with a separator no column
// value can contain, so two segments never collide in the lookup above.
func segmentKey(value, context string) string {
	return value + "\x00" + context
}
