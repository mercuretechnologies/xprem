// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// The log stream: the records the SDK ships, unioned with the native crashes
// only the manifest poll ever witnesses. Apart from explorer.go because it is
// a domain of its own, with its own cursor, its own severity vocabulary and
// its own second source, and it shares only the WHERE builder with the rest.
package observe

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"expo-open-ota/ee/identity"
)

type LogsQuery struct {
	ExplorerQuery
	Severity string
	Search   string
	// Exact event names, unlike Search which reads the body and the attributes
	// too. What a stream narrowed to "checkout_started" needs: a full-text
	// match would also return the exception that mentions it.
	EventNames []string
	Cursor     *LogCursor
	Limit      int
}

type LogCursor struct {
	Timestamp time.Time
	EventKey  uint64
}

type ObserveLog struct {
	EventKey       uint64    `json:"eventKey,string"`
	Timestamp      time.Time `json:"timestamp"`
	EASClientID    string    `json:"easClientId"`
	UpdateID       string    `json:"updateId"`
	Branch         string    `json:"branch"`
	Channel        string    `json:"channel"`
	RuntimeVersion string    `json:"runtimeVersion"`
	Platform       string    `json:"platform"`
	SessionID      string    `json:"sessionId"`
	EventName      string    `json:"eventName"`
	SeverityNumber uint8     `json:"severityNumber"`
	SeverityText   string    `json:"severityText"`
	IsFatal        bool      `json:"isFatal"`
	Body           string    `json:"body"`
	Attributes     string    `json:"attributes"`
	OSName         string    `json:"osName"`
	OSVersion      string    `json:"osVersion"`
	DeviceModel    string    `json:"deviceModel"`
	// Where the device was when the record was produced. Empty on records
	// ingested before the column existed, and wherever no GeoIP database is
	// configured.
	CountryCode    string `json:"countryCode"`
	AppVersion     string `json:"appVersion"`
	AppBuildNumber string `json:"appBuildNumber"`
	EASBuildID     string `json:"easBuildId"`
	Environment    string `json:"environment"`
	SDKVersion     string `json:"sdkVersion"`
}

type LogsPage struct {
	Available  bool         `json:"available"`
	Logs       []ObserveLog `json:"logs"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

func severityPredicate(severity string) sqlFragment {
	switch severity {
	case "fatal":
		return "l.is_fatal = 1"
	case "error":
		return "l.severity_number >= 17"
	case "warn":
		return "l.severity_number >= 13 AND l.severity_number < 17"
	case "info":
		return "l.severity_number >= 9 AND l.severity_number < 13"
	case "debug":
		return "l.severity_number < 9"
	default:
		return ""
	}
}

// nativeCrashArm builds the half of the log stream the SDK cannot report. A
// crash at launch kills the app before any instrumentation runs, so the only
// witness is the manifest poll that follows, and the record lives in
// device_health_events. It is shown next to the app's own records because
// "why did this update fail" is one question, not two.
//
// The arm drops out entirely rather than half-answering: a filter on a
// dimension the manifest poll never learns (channel, app version, build,
// environment) cannot be honoured here, and a severity filter that excludes
// errors excludes every one of these by definition.
func nativeCrashArm(query LogsQuery, cohort bool) (sqlFragment, []any, bool) {
	if len(query.Channels) > 0 || len(query.AppVersions) > 0 || len(query.AppBuildNumbers) > 0 ||
		len(query.EASBuildIDs) > 0 || len(query.Environments) > 0 {
		return "", nil, false
	}
	switch query.Severity {
	case "", "fatal", "error":
	default:
		return "", nil, false
	}
	// These records answer to one name only, so a stream narrowed to any other
	// event is not asking for them.
	if len(query.EventNames) > 0 && !slices.Contains(query.EventNames, nativeCrashEventName) {
		return "", nil, false
	}

	where := sqlFragment("h.app_id = ? AND h.occurred_at >= ? AND h.occurred_at <= ?" +
		" AND h.failure_type = ?")
	args := []any{query.From.UTC(), query.To.UTC(), string(identity.FailureTypeUpdate)}
	inFilter := func(column sqlFragment, values []string) {
		if len(values) == 0 {
			return
		}
		where += " AND h." + column + " IN ?"
		args = append(args, values)
	}
	inFilter("platform", query.Platform)
	inFilter("update_id", query.UpdateIDs)
	// The table has no group column: the group was already resolved to the
	// updates it contains, which is the same question asked of this row. Kept
	// as its own predicate rather than merged into the update ids, so that
	// asking for an update AND a group stays an intersection here too, exactly
	// like the telemetry arm. A group that resolved to nothing matches nothing,
	// which an empty IN list would not say.
	if len(query.UpdateGroupIDs) > 0 {
		if len(query.MemberUpdateIDs) == 0 {
			return "", nil, false
		}
		inFilter("update_id", query.MemberUpdateIDs)
	}
	inFilter("branch", query.Branches)
	inFilter("runtime_version", query.RuntimeVersions)
	inFilter("eas_client_id", query.EASClientIDs)
	inFilter("os_name", query.OSNames)
	inFilter("os_version", query.OSVersions)
	inFilter("device_model", query.DeviceModels)
	inFilter("country_code", query.CountryCodes)
	if cohort {
		where += " AND h.eas_client_id IN (SELECT eas_client_id FROM observe_identity_cohort)"
	}
	if query.Search != "" {
		where += " AND positionCaseInsensitiveUTF8(concat(?, ' ', h.fatal_error), ?) > 0"
		args = append(args, nativeCrashEventName, query.Search)
	}
	return where, args, true
}

// The event name these records answer to, and the body they carry when the
// device reported no error text: a crash with no message is still a crash,
// and an empty row would read as a logging bug.
const (
	nativeCrashEventName = "native_crash"
	nativeCrashFallback  = "Native crash at launch, no error reported"
)

func (e *Explorer) ReadLogs(ctx context.Context, appID string, query LogsQuery) (LogsPage, error) {
	page := LogsPage{Available: e.clickhouse != nil, Logs: []ObserveLog{}}
	if e.clickhouse == nil {
		return page, nil
	}
	queryContext, resolved, empty, err := e.prepareTelemetryRead(ctx, appID, query.ExplorerQuery)
	if err != nil {
		return LogsPage{}, err
	}
	query.ExplorerQuery = resolved
	if empty {
		return page, nil
	}
	cohort := len(query.MetadataFilter) > 0
	where, args := telemetryWhere("l", query.ExplorerQuery, cohort)
	if predicate := severityPredicate(query.Severity); predicate != "" {
		where += " AND " + predicate
	}
	if len(query.EventNames) > 0 {
		where += " AND l.event_name IN ?"
		args = append(args, query.EventNames)
	}
	if query.Search != "" {
		where += " AND positionCaseInsensitiveUTF8(concat(l.event_name, ' ', l.body, ' ', l.attributes), ?) > 0"
		args = append(args, query.Search)
	}

	// The cursor applies twice: once inside each arm to bound the scan, once
	// outside to page across their merge. Its outer arguments are kept apart
	// because they are bound after both arms, whatever the arms contain.
	var outerWhere sqlFragment
	var outerArgs []any
	if query.Cursor != nil {
		where += " AND l.timestamp <= ?"
		args = append(args, query.Cursor.Timestamp.UTC())
		outerWhere = "WHERE timestamp < ? OR (timestamp = ? AND event_key < ?)"
		outerArgs = []any{query.Cursor.Timestamp.UTC(), query.Cursor.Timestamp.UTC(), query.Cursor.EventKey}
	}
	// Built before the logs args are consumed: the two arms share one flat
	// placeholder list, so the native one has to be appended in the order it
	// appears in the statement.
	nativeWhere, nativeArgs, withNative := nativeCrashArm(query, cohort)
	if withNative && query.Cursor != nil {
		nativeWhere += " AND h.occurred_at <= ?"
		nativeArgs = append(nativeArgs, query.Cursor.Timestamp.UTC())
	}
	var nativeSQL sqlFragment
	if withNative {
		nativeSQL = `
			UNION ALL
			SELECT
				outbox_id AS event_key,
				argMax(occurred_at, ingested_at) AS timestamp,
				argMax(eas_client_id, ingested_at) AS eas_client_id,
				argMax(update_id, ingested_at) AS update_id,
				argMax(branch, ingested_at) AS branch,
				'' AS channel,
				argMax(runtime_version, ingested_at) AS runtime_version,
				argMax(platform, ingested_at) AS platform,
				toUUID('00000000-0000-0000-0000-000000000000') AS session_id,
				'` + nativeCrashEventName + `' AS event_name,
				CAST(21 AS UInt8) AS severity_number,
				'FATAL' AS severity_text,
				CAST(1 AS UInt8) AS is_fatal,
				if(argMax(fatal_error, ingested_at) = '', '` + nativeCrashFallback + `',
				   argMax(fatal_error, ingested_at)) AS body,
				'' AS attributes,
				argMax(os_name, ingested_at) AS os_name,
				argMax(os_version, ingested_at) AS os_version,
				argMax(device_model, ingested_at) AS device_model,
				argMax(country_code, ingested_at) AS country_code,
				'' AS app_version,
				'' AS app_build_number,
				'' AS eas_build_id,
				'' AS environment,
				'' AS sdk_version
			FROM device_health_events h
			WHERE ` + nativeWhere + `
			GROUP BY outbox_id`
	}

	sql := sqlf(`
		SELECT event_key, timestamp, toString(eas_client_id), toString(update_id),
		       branch, channel, runtime_version, platform, toString(session_id),
		       event_name, severity_number, severity_text, is_fatal, body,
		       attributes, os_name, os_version, device_model, country_code,
		       app_version, app_build_number, eas_build_id, environment, sdk_version
		FROM (
			SELECT
				event_key,
				argMax(timestamp, ingested_at) AS timestamp,
				argMax(eas_client_id, ingested_at) AS eas_client_id,
				argMax(update_id, ingested_at) AS update_id,
				argMax(branch, ingested_at) AS branch,
				argMax(channel, ingested_at) AS channel,
				argMax(runtime_version, ingested_at) AS runtime_version,
				argMax(platform, ingested_at) AS platform,
				argMax(session_id, ingested_at) AS session_id,
				argMax(event_name, ingested_at) AS event_name,
				argMax(severity_number, ingested_at) AS severity_number,
				argMax(severity_text, ingested_at) AS severity_text,
				argMax(is_fatal, ingested_at) AS is_fatal,
				argMax(body, ingested_at) AS body,
				argMax(attributes, ingested_at) AS attributes,
				argMax(os_name, ingested_at) AS os_name,
				argMax(os_version, ingested_at) AS os_version,
				argMax(device_model, ingested_at) AS device_model,
				argMax(country_code, ingested_at) AS country_code,
				argMax(app_version, ingested_at) AS app_version,
				argMax(app_build_number, ingested_at) AS app_build_number,
				argMax(eas_build_id, ingested_at) AS eas_build_id,
				argMax(environment, ingested_at) AS environment,
				argMax(sdk_version, ingested_at) AS sdk_version
			FROM (
				SELECT l.*,
				       if(content_hash = 0,
				          cityHash64(toString(eas_client_id), toString(session_id), event_name, body, toString(timestamp)),
				          content_hash) AS event_key
				FROM observe_logs l
				WHERE %s
			)
			GROUP BY event_key
		%s
		)
		%s
		ORDER BY timestamp DESC, event_key DESC
		LIMIT ?`, where, nativeSQL, outerWhere)
	args = prependAppID(appID, args)
	if withNative {
		args = append(args, prependAppID(appID, nativeArgs)...)
	}
	args = append(args, outerArgs...)
	args = append(args, query.Limit+1)
	rows, err := e.clickhouse.Conn.Query(queryContext, sql, args...)
	if err != nil {
		return LogsPage{}, fmt.Errorf("reading observe logs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row ObserveLog
		var fatal uint8
		if err := rows.Scan(
			&row.EventKey, &row.Timestamp, &row.EASClientID, &row.UpdateID,
			&row.Branch, &row.Channel, &row.RuntimeVersion, &row.Platform,
			&row.SessionID, &row.EventName, &row.SeverityNumber, &row.SeverityText,
			&fatal, &row.Body, &row.Attributes, &row.OSName, &row.OSVersion,
			&row.DeviceModel, &row.CountryCode, &row.AppVersion, &row.AppBuildNumber, &row.EASBuildID,
			&row.Environment, &row.SDKVersion,
		); err != nil {
			return LogsPage{}, err
		}
		row.IsFatal = fatal == 1
		page.Logs = append(page.Logs, row)
	}
	if err := rows.Err(); err != nil {
		return LogsPage{}, err
	}
	if len(page.Logs) > query.Limit {
		page.Logs = page.Logs[:query.Limit]
		last := page.Logs[len(page.Logs)-1]
		page.NextCursor = EncodeLogCursor(LogCursor{Timestamp: last.Timestamp, EventKey: last.EventKey})
	}
	return page, nil
}

func EncodeLogCursor(cursor LogCursor) string {
	raw := cursor.Timestamp.UTC().Format(time.RFC3339Nano) + "|" + fmt.Sprint(cursor.EventKey)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func DecodeLogCursor(raw string) (*LogCursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid cursor")
	}
	timestamp, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, err
	}
	// ParseUint and not Sscan: Sscan stops at the first byte it cannot use and
	// reports success on what it read, so "42junk" decodes to 42 and "0x10" to
	// 16. A corrupted cursor would then page from a position nobody asked for
	// instead of being refused.
	eventKey, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor key: %w", err)
	}
	return &LogCursor{Timestamp: timestamp, EventKey: eventKey}, nil
}
