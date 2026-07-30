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

// BreakdownSegment is one row of a breakdown: a segment value with the percentiles and device count behind it.
type BreakdownSegment struct {
	// Value doubles as the filter to apply when drilling into the segment.
	Value string `json:"value"`
	// Context qualifies the value when it means nothing alone, e.g. an OS version needs its OS name.
	Context string  `json:"context,omitempty"`
	Devices uint64  `json:"devices"`
	Samples uint64  `json:"samples"`
	P50     float64 `json:"p50"`
	P90     float64 `json:"p90"`
	// Points is the median-over-time series, present only when the caller asked for it.
	Points []ObserveMetricPoint `json:"points,omitempty"`
}

// Breakdown groups one metric by one dimension into ranked segments.
type Breakdown struct {
	Available bool               `json:"available"`
	Metric    string             `json:"metric"`
	Dimension string             `json:"dimension"`
	Segments  []BreakdownSegment `json:"segments"`
	// Overall is the same metric over the same filters with no grouping, the baseline segments deviate from.
	Overall BreakdownSegment `json:"overall"`
}

// BreakdownQuery parameterizes a breakdown request.
type BreakdownQuery struct {
	ExplorerQuery
	// Metric is a definition ID from observedMetricDefinitions, resolved to a metric_name before it reaches SQL.
	Metric string
	// Dimension is the single column the segments group on. Empty is rejected.
	Dimension string
	Limit     int
	// WithPoints fills Segment.Points with a median-per-bucket series.
	WithPoints bool
}

// paramString reads one key of the params the client attached to a data point.
func paramString(key string) sqlFragment {
	return sqlFragment(fmt.Sprintf("JSONExtractString(m.custom_params, '%s')", key))
}

// paramBool turns a boolean param into a readable label instead of "true"/"false".
func paramBool(key, whenTrue, whenFalse string) sqlFragment {
	return sqlFragment(fmt.Sprintf(
		"if(JSONHas(m.custom_params, '%s'), if(JSONExtractBool(m.custom_params, '%s'), '%s', '%s'), '')",
		key, key, whenTrue, whenFalse,
	))
}

// paramBuckets turns a numeric param into named ranges. labels carries one more entry than bounds,
// for the open-ended range above the last bound.
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

// breakdownDimensions is the allowlist of dimension name to the columns it groups on: the value lands in the
// SQL string itself, so anything outside this table must never reach the query builder.
var breakdownDimensions = map[string]struct {
	column  sqlFragment
	context sqlFragment
	// expr replaces the column for a dimension read from the params JSON rather than a column.
	expr sqlFragment
	// values is what a filter on this dimension accepts, in picker order. Set on the conditions only.
	values []string
	// session says the value belongs to the session rather than to the row, and reading it costs a join.
	session bool
}{
	"deviceModel": {column: "device_model"},
	// route_name is empty on app-wide timings (expo-router integration only).
	"route":          {column: "route_name"},
	"osVersion":      {column: "os_version", context: "os_name"},
	"osName":         {column: "os_name"},
	"country":        {column: "country_code"},
	"appVersion":     {column: "app_version"},
	"appBuildNumber": {column: "app_build_number"},
	"update":         {column: "update_id"},
	// The publish, not the per-platform row: grouping by update would split one publish in two.
	"updateGroup":    {column: "update_group_id"},
	"branch":         {column: "branch"},
	"runtimeVersion": {column: "runtime_version"},
	"channel":        {column: "channel"},
	"environment":    {column: "environment"},
	"platform":       {column: "platform"},
	// Attached by the framework to interactive timings only.
	"thermalState": {expr: paramString("expo.device.thermalState"), values: thermalStates},
	// Read off the session: an update download has no network condition of its own, so it borrows the session's.
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
	// interactiveMetric is the one metric the client attaches conditions to; everything session-scoped reads from it.
	interactiveMetric = "expo.app_startup.tti"
	// sessionNetwork is the alias the join below is given.
	sessionNetwork sqlFragment = "session_state.network"
)

// metricsSource joins the session's own interactive timing when a session-scoped condition is needed.
// LEFT, so a session that never became interactive still contributes rows with an empty network.
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

// The values each condition can take, worst last so a picker reads as a scale.
var (
	thermalStates = []string{"nominal", "fair", "serious", "critical"}
	networkTypes  = []string{"wifi", "cellular", "none"}
	powerModes    = []string{lowPowerOff, lowPowerOn}
	frozenFrames  = []string{"None", "1 to 2", "3 to 8", "9 to 20", "Over 20"}
	networkBytes  = []string{"Under 100 kB", "100 to 500 kB", "500 kB to 2 MB", "Over 2 MB"}
)

// ConditionDefinition is one filterable condition and the values it takes.
type ConditionDefinition struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
	// SessionScoped says the condition is read off the session rather than off the measurement.
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

// ConditionDimensions names the dimensions that read a measurement's own params rather than a column.
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

// conditionsWhere narrows to the measurements taken in a given state. Applied on the timing reads only.
func conditionsWhere(conditions map[string][]string) (sqlFragment, []any) {
	if len(conditions) == 0 {
		return "", nil
	}
	var where sqlFragment
	var args []any
	rowScoped := false
	// Sorted, not map order, so the same filters always produce the same SQL.
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
	// maxBreakdownSeries caps how many series get a points query, so a chart doesn't turn into a hairball.
	maxBreakdownSeries = 8
	// minDevicesToRank excludes segments too small for their median to mean anything.
	minDevicesToRank = 5
)

// plottableSegments picks the series worth drawing, ranked by impact rather than by sample count.
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

// breakdownGrouping is the pair of columns a dimension groups by: the value itself and its context qualifier.
type breakdownGrouping struct {
	column  sqlFragment
	context sqlFragment
}

// groupingSQL builds the aliases shared by both queries below. `selected` is wrapped in any() because the
// inner query already collapsed duplicate data points onto one value per dedup key.
func groupingSQL(grouping breakdownGrouping) (selected, aliases []sqlFragment) {
	return []sqlFragment{
			sqlFragment(sqlf("any(%s) AS s0", grouping.column)),
			sqlFragment(sqlf("any(%s) AS c0", grouping.context)),
		},
		[]sqlFragment{"s0", "c0"}
}

// dedupKey collapses a data point re-sent by a retried batch onto the one already counted.
const dedupKey sqlFragment = `m.content_key`

// ReadBreakdown is the cached entry point for readBreakdown.
func (e *Explorer) ReadBreakdown(
	ctx context.Context,
	appID string,
	query BreakdownQuery,
) (Breakdown, error) {
	return cachedRead(
		ctx,
		readCacheKey("breakdown", appID, query),
		func(ctx context.Context) (Breakdown, error) { return e.readBreakdown(ctx, appID, query) })
}

func (e *Explorer) readBreakdown(
	ctx context.Context,
	appID string,
	query BreakdownQuery,
) (Breakdown, error) {
	dimension, found := breakdownDimensions[query.Dimension]
	if query.Dimension == "" || !found {
		return Breakdown{}, errInvalidObserveFilter
	}
	var grouping breakdownGrouping
	// condition is set when the dimension only exists where the client attached params.
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
	// Splitting on a condition drops samples that never reported one, so they don't collapse into a nameless
	// segment that skews the baseline.
	if rowScopedSplit {
		where += " AND m.custom_params != ''"
	}
	if condition != "" {
		where += " AND " + condition + " != ''"
	}
	source, sourceArgs := metricsSource(appID, query.ExplorerQuery, query.Dimension)
	selected, aliases := groupingSQL(grouping)

	// uniq, not uniqExact: an exact distinct count over millions of rows costs far more for a figure nobody reconciles to the unit.
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

	// Percentiles can't be averaged back from the segments, so the baseline is its own query.
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

// readBreakdownPoints fills the median-per-bucket series of the top-ranked segments only.
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
	// segments is the set filled in place.
	segments []BreakdownSegment,
	baselineP50 float64,
) error {
	plotted := plottableSegments(segments, baselineP50)
	if len(plotted) == 0 {
		return nil
	}

	// The value and its context travel as a pair: an OS version alone matches the same string under another OS name.
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

// finite replaces the percentile of an empty set (ClickHouse's NaN, which JSON can't marshal) with zero.
func finite(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

// segmentKey joins a segment's value and context with a separator no column value can contain.
func segmentKey(value, context string) string {
	return value + "\x00" + context
}

// BreakdownDimensions names every dimension a breakdown may split on, sorted.
// Exported for the surfaces that have to tell a caller what is available.
func BreakdownDimensions() []string {
	names := make([]string, 0, len(breakdownDimensions))
	for name := range breakdownDimensions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
