// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/ext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/clickhouse"
	"expo-open-ota/internal/database/postgres/pgdb"
)

const embeddedUpdateID = "00000000-0000-0000-0000-000000000000"

var observedMetricDefinitions = []MetricDefinition{
	{
		ID: "cold-launch", Name: "expo.app_startup.cold_launch_time", Label: "Cold launch", Unit: "s",
		Description: "Process start to the first frame of the root view, when the app was not already running. Prewarmed launches are not measured on iOS.",
		Category:    "startup", MinimumSDK: 55,
	},
	{
		ID: "warm-launch", Name: "expo.app_startup.warm_launch_time", Label: "Warm launch", Unit: "s",
		Description: "The same measurement when the process was still alive and only the interface had to come back, so it skips everything a cold start pays for.",
		Category:    "startup", MinimumSDK: 55,
	},
	{
		ID: "bundle-load", Name: "expo.app_startup.bundle_load_time", Label: "Bundle load", Unit: "s",
		Description: "How long evaluating the JavaScript bundle took. It happens inside a cold launch rather than after it, so it is a share of that number, not an extra wait.",
		Category:    "startup", MinimumSDK: 55,
	},
	{
		ID: "first-render", Name: "expo.app_startup.ttr", Label: "Time to first render", Unit: "s",
		Description: "Launch to the first frame your root component paints. Marked automatically when ObserveRoot mounts.",
		Category:    "startup", MinimumSDK: 55,
	},
	{
		ID: "navigation-cold", Name: "expo.navigation.cold_ttr", Label: "Cold navigation", Unit: "s",
		Description: "Opening a screen for the first time in a session, until it renders.",
		Category:    "navigation", MinimumSDK: 56,
	},
	{
		ID: "navigation-warm", Name: "expo.navigation.warm_ttr", Label: "Warm navigation", Unit: "s",
		Description: "Coming back to a screen already visited in that session, which skips the work the first visit paid for.",
		Category:    "navigation", MinimumSDK: 56,
	},
	{
		ID: "interactive", Name: "expo.app_startup.tti", Label: "Time to interactive", Unit: "s",
		Description: "Launch to the moment the app can actually be used, marked by your own call to markInteractive(). The one timing that carries the state the device was in.",
		Category:    "startup", MinimumSDK: 55,
	},
	{
		ID: "navigation-interactive", Name: "expo.navigation.tti", Label: "Navigation interactive", Unit: "s",
		Description: "A screen from opening to usable. Needs a router integration enabled in configure().",
		Category:    "navigation", MinimumSDK: 56,
	},
	{
		ID: "update-download", Name: "expo.updates.download_time", Label: "Update download", Unit: "s",
		Description: "How long fetching an update took, measured when expo-updates finishes downloading it. Only the devices that received one report it, so the count is smaller than the fleet.",
		Category:    "updates", MinimumSDK: 55,
	},
	{
		ID: "legacy-load", Name: "expo.app_startup.load_time", Label: "App load (legacy)", Unit: "s",
		Description: "Reported by older iOS clients only, kept so a fleet still running them is not silently missing from this page.",
		Category:    "startup", MinimumSDK: 55,
	},
	{
		ID: "legacy-launch", Name: "expo.app_startup.launch_time", Label: "App launch (legacy)", Unit: "s",
		Description: "Reported by older iOS clients only, kept so a fleet still running them is not silently missing from this page.",
		Category:    "startup", MinimumSDK: 55,
	},
}

type ExplorerQuery struct {
	From time.Time
	To   time.Time
	// Every dimension is a set: empty means "do not filter", one value is the
	// common case, and several is what turns a filter into a comparison.
	Platform  []string
	UpdateIDs []string
	// UpdateGroupIDs are the groups asked for; MemberUpdateIDs are the update
	// ids they resolve to, kept apart because rows ingested before the group
	// column existed can only be found through their members.
	UpdateGroupIDs  []string
	MemberUpdateIDs []string
	Branches        []string
	RuntimeVersions []string
	Channels        []string
	EASClientIDs    []string
	AppVersions     []string
	AppBuildNumbers []string
	EASBuildIDs     []string
	Environments    []string
	// Hardware and OS dimensions. They ride on every telemetry row already
	// (flatten.go fills them from device.model.identifier, os.name and
	// os.version), and they are what makes "is this release slower on old
	// Android phones" answerable.
	OSNames      []string
	OSVersions   []string
	DeviceModels []string
	// CountryCode is the country frozen on the row at ingestion, not the
	// device's current country: filtering on it answers "where was it slow",
	// not "where is that phone now". The rows also carry the city centroid
	// coordinates, which nothing queries yet.
	CountryCodes   []string
	MetadataFilter [][]byte
	// Conditions narrows on the state the device reported for a measurement,
	// keyed by breakdown dimension name. Deliberately apart from the dimensions
	// above: those describe a device and hold on every row it ever sent, while a
	// thermal state describes one measurement. Only the timing reads honor them,
	// so a page that cannot must not offer them.
	Conditions map[string][]string
	Bucket     time.Duration
}
type MetricDefinition struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Label string `json:"label"`
	// What the timing actually measures, in one sentence, for the dashboard to
	// show next to the chart. Written here rather than there because what a
	// metric means is a property of the client that reports it, and a label
	// alone ("Warm launch") does not say what was and was not counted.
	Description string `json:"description"`
	Unit        string `json:"unit"`
	Category    string `json:"category"`
	MinimumSDK  int    `json:"minimumSdk"`
}

type ObserveMetricPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

type MetricStats struct {
	Count  uint64  `json:"count"`
	Median float64 `json:"median"`
	Avg    float64 `json:"avg"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	P90    float64 `json:"p90"`
	P99    float64 `json:"p99"`
	// Distinct installs behind the samples. A count of samples alone reads as
	// a fleet-wide number when it can just as well be one device reporting a
	// thousand times.
	Devices uint64 `json:"devices"`
	// Whether any sample carried the state the device was in. Probed on one of
	// the framework keys rather than on "has params at all": a navigation timing
	// carries its route params and none of the conditions, so the looser test
	// would promise a split that comes back empty. Read from the data rather
	// than declared per metric because which timings carry them is the client's
	// choice and it changes with the SDK.
	ReportsConditions bool `json:"reportsConditions"`
}

type MetricSeries struct {
	MetricDefinition
	Stats  MetricStats          `json:"stats"`
	Points []ObserveMetricPoint `json:"points"`
}

type ObserveSummary struct {
	Users     uint64   `json:"users"`
	Releases  uint64   `json:"releases"`
	Builds    uint64   `json:"builds"`
	Updates   uint64   `json:"updates"`
	Sessions  uint64   `json:"sessions"`
	Events    uint64   `json:"events"`
	Platforms []string `json:"platforms"`
}
type Overview struct {
	Available bool              `json:"available"`
	Summary   ObserveSummary    `json:"summary"`
	Metrics   []MetricSeries    `json:"metrics"`
	Locations []ObserveLocation `json:"locations"`
}

// observeCohortLimit caps the identity cohort an attribute filter resolves to.
// The whole list is held in memory and copied onto the ClickHouse connection
// as an external table on every request, so it is sized to stay a few
// megabytes rather than to cover any fleet: past it the filter is close enough
// to "everyone" that asking for a narrower one is the better answer.
const observeCohortLimit = 200_000

type ObserveEventPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Count     uint64    `json:"count"`
}

type ObserveEventSeries struct {
	Name     string              `json:"name"`
	Count    uint64              `json:"count"`
	Users    uint64              `json:"users"`
	Sessions uint64              `json:"sessions"`
	Points   []ObserveEventPoint `json:"points"`
}

type Events struct {
	Available bool                 `json:"available"`
	Events    []ObserveEventSeries `json:"events"`
}

// Explorer queries telemetry facts from ClickHouse and the mutable geo /
// identity dimension from PostgreSQL. ClickHouse may be nil: the map remains
// useful while the metrics and logs views report available=false.
type Explorer struct {
	postgres   *database.Engine
	clickhouse *clickhouse.Engine
}

func NewExplorer(postgres *database.Engine, clickhouse *clickhouse.Engine) *Explorer {
	if postgres == nil {
		return nil
	}
	return &Explorer{postgres: postgres, clickhouse: clickhouse}
}

func toPGUUID(value string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

func toPGUUIDs(values []string) ([]pgtype.UUID, error) {
	out := make([]pgtype.UUID, 0, len(values))
	for _, value := range values {
		parsed, err := toPGUUID(value)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed)
	}
	return out, nil
}

func (e *Explorer) cohortContext(ctx context.Context, appID string, activeSince time.Time, filters [][]byte) (context.Context, bool, error) {
	if len(filters) == 0 {
		return ctx, false, nil
	}
	appUUID, err := toPGUUID(appID)
	if err != nil {
		return ctx, false, err
	}
	ids, err := e.postgres.ListObserveCohortDeviceIDs(ctx, pgdb.ListObserveCohortDeviceIDsParams{
		AppID:       appUUID,
		ActiveSince: pgtype.Timestamptz{Time: activeSince.UTC(), Valid: true},
		Filters:     filters,
		// One over the cap, so "exactly at the cap" is not mistaken for
		// "truncated".
		Lim: observeCohortLimit + 1,
	})
	if err != nil {
		return ctx, false, fmt.Errorf("listing observe cohort: %w", err)
	}
	if len(ids) > observeCohortLimit {
		return ctx, false, errObserveCohortTooLarge
	}
	table, err := ext.NewTable("observe_identity_cohort", ext.Column("eas_client_id", "UUID"))
	if err != nil {
		return ctx, false, fmt.Errorf("creating observe cohort: %w", err)
	}
	for _, id := range ids {
		if err := table.Append(uuid.UUID(id.Bytes)); err != nil {
			return ctx, false, fmt.Errorf("building observe cohort: %w", err)
		}
	}
	return chdriver.Context(ctx, chdriver.WithExternalTable(table)), len(ids) == 0, nil
}

// prepareTelemetryRead runs the two preparations every ClickHouse read needs:
// update groups are resolved to the concrete update ids the rows carry, and the
// identity cohort behind an attribute filter is installed on the connection as
// an external table. The bool says the filters cannot match a single row, in
// which case the caller answers with its own empty shape rather than asking
// ClickHouse a question with a known answer.
//
// Deliberately not used by ReadOverview: it reads Postgres between the two
// steps and returns early when there is no ClickHouse at all, so going through
// here would make it pay for a cohort lookup it never uses.
func (e *Explorer) prepareTelemetryRead(
	ctx context.Context,
	appID string,
	query ExplorerQuery,
) (context.Context, ExplorerQuery, bool, error) {
	resolved, emptyUpdateGroup, err := e.resolveUpdateGroup(ctx, appID, query)
	if err != nil {
		return ctx, query, false, err
	}
	if emptyUpdateGroup {
		return ctx, resolved, true, nil
	}
	queryContext, emptyCohort, err := e.cohortContext(ctx, appID, resolved.From, resolved.MetadataFilter)
	if err != nil {
		return ctx, resolved, false, err
	}
	return queryContext, resolved, emptyCohort, nil
}

// resolveUpdateGroup collects the update ids behind the requested groups. One
// query per group: a comparison rarely holds more than a handful, and the
// lookup is a single indexed row set each time.
func (e *Explorer) resolveUpdateGroup(ctx context.Context, appID string, query ExplorerQuery) (ExplorerQuery, bool, error) {
	if len(query.UpdateGroupIDs) == 0 {
		return query, false, nil
	}
	appUUID, err := toPGUUID(appID)
	if err != nil {
		return query, false, err
	}
	members := make([]string, 0, 2*len(query.UpdateGroupIDs))
	for _, group := range query.UpdateGroupIDs {
		groupUUID, err := toPGUUID(group)
		if err != nil {
			return query, false, err
		}
		ids, err := e.postgres.ListObserveUpdateUUIDsByPublishGroup(
			ctx,
			pgdb.ListObserveUpdateUUIDsByPublishGroupParams{
				AppID:        appUUID,
				PublishGroup: groupUUID,
			},
		)
		if err != nil {
			return query, false, fmt.Errorf("resolving Observe update group: %w", err)
		}
		for _, id := range ids {
			members = append(members, uuid.UUID(id.Bytes).String())
		}
	}
	query.MemberUpdateIDs = members
	// Groups that resolve to nothing at all: the answer is empty, and saying so
	// beats a query that silently matches every row.
	return query, len(members) == 0, nil
}

func telemetryWhere(table sqlFragment, query ExplorerQuery, cohort bool) (sqlFragment, []any) {
	// app_id is prepended by callers so unions can reuse this helper cleanly.
	where := table + ".app_id = ? AND " + table + ".timestamp >= ? AND " + table + ".timestamp <= ?"
	args := []any{query.From.UTC(), query.To.UTC()}
	// One value or twenty, the predicate is the same shape. IN with a single
	// element costs nothing and keeps this readable.
	inFilter := func(column sqlFragment, values []string) {
		if len(values) == 0 {
			return
		}
		where += " AND " + table + "." + column + " IN ?"
		args = append(args, values)
	}
	inFilter("platform", query.Platform)
	inFilter("update_id", query.UpdateIDs)
	if len(query.UpdateGroupIDs) > 0 {
		// Rows ingested before update_group_id existed carry the zero uuid, so
		// the member ids stay in the predicate: dropping them would silently
		// hide every sample older than the column.
		where += " AND (" + table + ".update_group_id IN ? OR " + table + ".update_id IN ?)"
		args = append(args, query.UpdateGroupIDs, query.MemberUpdateIDs)
	}
	inFilter("branch", query.Branches)
	inFilter("runtime_version", query.RuntimeVersions)
	inFilter("channel", query.Channels)
	inFilter("eas_client_id", query.EASClientIDs)
	inFilter("app_version", query.AppVersions)
	inFilter("app_build_number", query.AppBuildNumbers)
	inFilter("eas_build_id", query.EASBuildIDs)
	inFilter("environment", query.Environments)
	inFilter("os_name", query.OSNames)
	inFilter("os_version", query.OSVersions)
	inFilter("device_model", query.DeviceModels)
	inFilter("country_code", query.CountryCodes)
	if cohort {
		where += " AND " + table + ".eas_client_id IN (SELECT eas_client_id FROM observe_identity_cohort)"
	}
	return where, args
}

func prependAppID(appID string, args []any) []any {
	return append([]any{appID}, args...)
}

func emptyMetricSeries() []MetricSeries {
	metrics := make([]MetricSeries, 0, len(observedMetricDefinitions))
	for _, definition := range observedMetricDefinitions {
		metrics = append(metrics, MetricSeries{
			MetricDefinition: definition,
			Points:           []ObserveMetricPoint{},
		})
	}
	return metrics
}

func (e *Explorer) ReadOverview(ctx context.Context, appID string, query ExplorerQuery) (Overview, error) {
	resolvedQuery, emptyUpdateGroup, err := e.resolveUpdateGroup(ctx, appID, query)
	if err != nil {
		return Overview{}, err
	}
	query = resolvedQuery
	if emptyUpdateGroup {
		return Overview{
			Available: e.clickhouse != nil,
			Metrics:   []MetricSeries{},
			Locations: []ObserveLocation{},
		}, nil
	}
	locations, err := e.locations(ctx, appID, query.From, query)
	if err != nil {
		return Overview{}, err
	}
	activeUsers, err := e.activeUsers(ctx, appID, query)
	if err != nil {
		return Overview{}, err
	}
	overview := Overview{
		Available: e.clickhouse != nil,
		Summary:   ObserveSummary{Users: activeUsers},
		Metrics:   []MetricSeries{},
		Locations: locations,
	}
	if e.clickhouse == nil {
		return overview, nil
	}

	queryContext, emptyCohort, err := e.cohortContext(ctx, appID, query.From, query.MetadataFilter)
	if err != nil {
		return Overview{}, err
	}
	if emptyCohort {
		return overview, nil
	}
	cohort := len(query.MetadataFilter) > 0
	if err := e.readSummary(queryContext, appID, query, cohort, &overview.Summary); err != nil {
		return Overview{}, err
	}
	// Deliberately sequential: each of these already fans out across every
	// core ClickHouse has, so issuing them together just makes them share the
	// same cores. Measured at a million devices, concurrency here changed
	// nothing; what the page cost was the queries themselves.
	metrics, err := e.readMetricStats(queryContext, appID, query, cohort)
	if err != nil {
		return Overview{}, err
	}
	points, err := e.readMetricPoints(queryContext, appID, query, cohort)
	if err != nil {
		return Overview{}, err
	}
	overview.Metrics = metrics
	for index := range overview.Metrics {
		overview.Metrics[index].Points = points[overview.Metrics[index].Name]
	}
	return overview, nil
}

// The conditions are deliberately NOT applied here, and neither are they in
// locations: they qualify one measurement (the state the device was in while a
// timing was taken), and this counts devices, sessions and events across both
// telemetry tables. observe_logs carries no such column, so narrowing the
// metrics arm alone would answer with a number that is neither filtered nor
// unfiltered. The summary and the map answer about the fleet; only the metric
// series answer about measurements, and those do apply them.
func (e *Explorer) readSummary(ctx context.Context, appID string, query ExplorerQuery, cohort bool, summary *ObserveSummary) error {
	metricsWhere, metricsArgs := telemetryWhere("m", query, cohort)
	logsWhere, logsArgs := telemetryWhere("l", query, cohort)
	// uniq for the two counted in the millions, uniqExact for the handful of
	// releases, builds and updates an app has: an exact distinct count over a
	// fleet costs more than every other figure on the page put together, and
	// nobody reconciles "active devices" to the unit. Deduplication groups on
	// one hashed key rather than on the ten columns it stands for, which is the
	// difference between a page that answers and a page that spins.
	sql := sqlf(`
		SELECT uniq(eas_client_id),
		       uniqExactIf(app_version, app_version != ''),
		       uniqExactIf(
		           if(eas_build_id != '', eas_build_id, concat(app_version, '#', app_build_number)),
		           eas_build_id != '' OR app_build_number != ''
		       ),
		       uniqExactIf(update_id, toString(update_id) != '%s'),
		       uniq(session_id),
		       countIf(is_event = 1),
		       groupUniqArray(platform)
		FROM (
			SELECT any(m.eas_client_id) AS eas_client_id, any(m.app_version) AS app_version,
			       any(m.app_build_number) AS app_build_number, any(m.eas_build_id) AS eas_build_id,
			       any(m.update_id) AS update_id, any(m.session_id) AS session_id,
			       any(m.platform) AS platform, 0 AS is_event
			FROM observe_metrics m WHERE %s
			GROUP BY %s
			UNION ALL
			SELECT any(l.eas_client_id), any(l.app_version), any(l.app_build_number),
			       any(l.eas_build_id), any(l.update_id), any(l.session_id),
			       any(l.platform), 1 AS is_event
			FROM observe_logs l WHERE %s
			GROUP BY if(l.content_hash = 0,
			            cityHash64(l.eas_client_id, l.session_id, l.event_name, l.timestamp),
			            l.content_hash)
		)`, embeddedUpdateID, metricsWhere, dedupKey, logsWhere)
	args := append(prependAppID(appID, metricsArgs), prependAppID(appID, logsArgs)...)
	if err := e.clickhouse.Conn.QueryRow(ctx, sql, args...).Scan(
		&summary.Users,
		&summary.Releases,
		&summary.Builds,
		&summary.Updates,
		&summary.Sessions,
		&summary.Events,
		&summary.Platforms,
	); err != nil {
		return fmt.Errorf("reading observe summary: %w", err)
	}
	return nil
}

func metricDefinition(name string) MetricDefinition {
	for _, definition := range observedMetricDefinitions {
		if definition.Name == name {
			return definition
		}
	}
	label := strings.TrimPrefix(name, "expo.unknown.")
	label = strings.ReplaceAll(label, "_", " ")
	id := strings.NewReplacer(".", "-", "/", "-", " ", "-").Replace(name)
	return MetricDefinition{
		ID:    id,
		Name:  name,
		Label: label,
		Description: "Reported by your app under a name expo-observe does not map to a known " +
			"timing, so it is shown exactly as it arrived.",
		Unit:       "s",
		Category:   "custom",
		MinimumSDK: 55,
	}
}

func (e *Explorer) readMetricStats(ctx context.Context, appID string, query ExplorerQuery, cohort bool) ([]MetricSeries, error) {
	where, args := telemetryWhere("m", query, cohort)
	conditionWhere, conditionArgs := conditionsWhere(query.Conditions)
	where += conditionWhere
	args = append(args, conditionArgs...)
	source, sourceArgs := metricsSource(appID, query, "")
	// uniq, not uniqExact: this is the "N devices" of a caption, and an exact
	// distinct count over millions of rows costs more than the rest of the
	// query put together for a figure nobody reconciles to the unit.
	sql := sqlf(`
		SELECT metric_name, count(), toFloat64(quantileTDigest(0.5)(value)), avg(value),
		       min(value), max(value), toFloat64(quantileTDigest(0.9)(value)),
		       toFloat64(quantileTDigest(0.99)(value)), uniq(eas_client_id),
		       max(has_conditions)
		FROM (
			SELECT any(m.metric_name) AS metric_name,
			       any(m.eas_client_id) AS eas_client_id,
			       any(m.value) AS value,
			       any(JSONHas(m.custom_params, 'expo.device.thermalState')) AS has_conditions
			FROM %s
			WHERE %s
			GROUP BY if(m.content_hash = 0,
			            cityHash64(m.eas_client_id, m.session_id, m.metric_name, m.timestamp, m.value),
			            m.content_hash)
		)
		GROUP BY metric_name
		ORDER BY count() DESC, metric_name
		LIMIT 100`, source, where)
	rows, err := e.clickhouse.Conn.Query(ctx, sql,
		append(sourceArgs, prependAppID(appID, args)...)...)
	if err != nil {
		return nil, fmt.Errorf("reading observe metric stats: %w", err)
	}
	defer rows.Close()
	metrics := make([]MetricSeries, 0)
	for rows.Next() {
		var name string
		var stats MetricStats
		if err := rows.Scan(
			&name, &stats.Count, &stats.Median, &stats.Avg, &stats.Min,
			&stats.Max, &stats.P90, &stats.P99, &stats.Devices,
			&stats.ReportsConditions,
		); err != nil {
			return nil, err
		}
		metrics = append(metrics, MetricSeries{
			MetricDefinition: metricDefinition(name),
			Stats:            stats,
			Points:           []ObserveMetricPoint{},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The cards come back in the order the definitions are written, not in the
	// order the fleet happened to report them: sorting by sample count reshuffles
	// the page every time the data moves, and it separates timings that only
	// make sense read next to each other. Anything unmapped keeps its place at
	// the end, ranked by volume, since there is no declared order to give it.
	sort.SliceStable(metrics, func(i, j int) bool {
		return definitionOrder(metrics[i].Name) < definitionOrder(metrics[j].Name)
	})
	return metrics, nil
}

// definitionOrder is where a metric sits in observedMetricDefinitions, and past
// the end for one it does not declare.
func definitionOrder(name string) int {
	for i, definition := range observedMetricDefinitions {
		if definition.Name == name {
			return i
		}
	}
	return len(observedMetricDefinitions)
}

func (e *Explorer) readMetricPoints(
	ctx context.Context,
	appID string,
	query ExplorerQuery,
	cohort bool,
) (map[string][]ObserveMetricPoint, error) {
	where, args := telemetryWhere("m", query, cohort)
	conditionWhere, conditionArgs := conditionsWhere(query.Conditions)
	where += conditionWhere
	args = append(args, conditionArgs...)
	source, sourceArgs := metricsSource(appID, query, "")
	sql := sqlf(`
		SELECT metric_name,
		       toStartOfInterval(timestamp, toIntervalSecond(?)) AS bucket,
		       toFloat64(quantileTDigest(0.5)(value))
		FROM (
			SELECT any(m.metric_name) AS metric_name,
			       any(m.timestamp) AS timestamp,
			       any(m.value) AS value
			FROM %s
			WHERE %s
			GROUP BY if(m.content_hash = 0,
			            cityHash64(m.eas_client_id, m.session_id, m.metric_name, m.timestamp, m.value),
			            m.content_hash)
		)
		GROUP BY metric_name, bucket
		ORDER BY metric_name, bucket`, source, where)
	queryArgs := []any{uint64(max(int64(query.Bucket/time.Second), 1))}
	queryArgs = append(queryArgs, sourceArgs...)
	queryArgs = append(queryArgs, prependAppID(appID, args)...)
	rows, err := e.clickhouse.Conn.Query(ctx, sql, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("reading observe metric points: %w", err)
	}
	defer rows.Close()
	points := map[string][]ObserveMetricPoint{}
	for rows.Next() {
		var name string
		var point ObserveMetricPoint
		if err := rows.Scan(&name, &point.Timestamp, &point.Value); err != nil {
			return nil, err
		}
		points[name] = append(points[name], point)
	}
	return points, rows.Err()
}

func (e *Explorer) ReadEvents(ctx context.Context, appID string, query ExplorerQuery) (Events, error) {
	events := Events{Available: e.clickhouse != nil, Events: []ObserveEventSeries{}}
	if e.clickhouse == nil {
		return events, nil
	}
	queryContext, resolved, empty, err := e.prepareTelemetryRead(ctx, appID, query)
	if err != nil {
		return Events{}, err
	}
	query = resolved
	if empty {
		return events, nil
	}
	cohort := len(query.MetadataFilter) > 0
	where, args := telemetryWhere("l", query, cohort)
	statsSQL := sqlf(`
		SELECT event_name, count(), uniqExact(eas_client_id), uniqExact(session_id)
		FROM (
			SELECT l.event_name, l.eas_client_id, l.session_id
			FROM observe_logs l
			WHERE %s
			GROUP BY l.event_name, l.eas_client_id, l.session_id, l.timestamp, l.content_hash
		)
		GROUP BY event_name
		ORDER BY count() DESC, event_name
		LIMIT 50`, where)
	rows, err := e.clickhouse.Conn.Query(queryContext, statsSQL, prependAppID(appID, args)...)
	if err != nil {
		return Events{}, fmt.Errorf("reading Observe event stats: %w", err)
	}
	for rows.Next() {
		var event ObserveEventSeries
		if err := rows.Scan(&event.Name, &event.Count, &event.Users, &event.Sessions); err != nil {
			rows.Close()
			return Events{}, err
		}
		event.Points = []ObserveEventPoint{}
		events.Events = append(events.Events, event)
	}
	if err := rows.Close(); err != nil {
		return Events{}, err
	}
	if len(events.Events) == 0 {
		return events, nil
	}

	names := make([]string, 0, len(events.Events))
	index := make(map[string]*ObserveEventSeries, len(events.Events))
	for i := range events.Events {
		names = append(names, events.Events[i].Name)
		index[events.Events[i].Name] = &events.Events[i]
	}
	pointsSQL := sqlf(`
		SELECT event_name,
		       toStartOfInterval(timestamp, toIntervalSecond(?)) AS bucket,
		       count()
		FROM (
			SELECT l.event_name, l.timestamp
			FROM observe_logs l
			WHERE %s AND l.event_name IN ?
			GROUP BY l.event_name, l.eas_client_id, l.session_id, l.timestamp, l.content_hash
		)
		GROUP BY event_name, bucket
		ORDER BY event_name, bucket`, where)
	queryArgs := []any{uint64(max(int64(query.Bucket/time.Second), 1))}
	queryArgs = append(queryArgs, prependAppID(appID, args)...)
	queryArgs = append(queryArgs, names)
	pointRows, err := e.clickhouse.Conn.Query(queryContext, pointsSQL, queryArgs...)
	if err != nil {
		return Events{}, fmt.Errorf("reading Observe event points: %w", err)
	}
	defer pointRows.Close()
	for pointRows.Next() {
		var name string
		var point ObserveEventPoint
		if err := pointRows.Scan(&name, &point.Timestamp, &point.Count); err != nil {
			return Events{}, err
		}
		if event := index[name]; event != nil {
			event.Points = append(event.Points, point)
		}
	}
	return events, pointRows.Err()
}
