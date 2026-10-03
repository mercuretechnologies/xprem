// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type ErrorDetailsQuery struct {
	ExplorerQuery
	Fatality string
	Cursor   *LogCursor
	Limit    int
}

type ErrorBreakdown struct {
	Key           string  `json:"key"`
	Label         string  `json:"label"`
	Occurrences   uint64  `json:"occurrences"`
	Percentage    float64 `json:"percentage"`
	UpdateID      string  `json:"updateId,omitempty"`
	UpdateGroupID string  `json:"updateGroupId,omitempty"`
	Platform      string  `json:"platform,omitempty"`
	OSName        string  `json:"osName,omitempty"`
	OSVersion     string  `json:"osVersion,omitempty"`
}

type ErrorDetails struct {
	Available                bool                `json:"available"`
	From                     time.Time           `json:"from"`
	To                       time.Time           `json:"to"`
	BucketSeconds            int64               `json:"bucketSeconds"`
	Summary                  *ErrorSummary       `json:"summary"`
	Series                   []ObserveEventPoint `json:"series"`
	Updates                  []ErrorBreakdown    `json:"updates"`
	DeviceModels             []ErrorBreakdown    `json:"deviceModels"`
	OSVersions               []ErrorBreakdown    `json:"osVersions"`
	Runtimes                 []ErrorBreakdown    `json:"runtimes"`
	Occurrences              []ObserveLog        `json:"occurrences"`
	NextCursor               string              `json:"nextCursor,omitempty"`
	RepresentativeOccurrence *ObserveLog         `json:"representativeOccurrence,omitempty"`
}

func (e *Explorer) ReadErrorDetails(ctx context.Context, appID, errorID string, query ErrorDetailsQuery) (ErrorDetails, error) {
	return cachedRead(ctx, errorDetailsReadCacheKey(appID, errorID, query), func(ctx context.Context) (ErrorDetails, error) { return e.readErrorDetails(ctx, appID, errorID, query) })
}

func errorDetailsReadCacheKey(appID, errorID string, query ErrorDetailsQuery) string {
	// fmt prints a pointer nested in a struct as its address. Key cursor pages
	// by their values so equivalent decoded cursors share an answer, and a
	// reused allocation cannot return a different occurrence page's cache.
	var cursor LogCursor
	hasCursor := query.Cursor != nil
	if hasCursor {
		cursor = *query.Cursor
	}
	query.Cursor = nil
	return readCacheKey("error-details", appID, errorID, query, hasCursor, cursor)
}

func (e *Explorer) readErrorDetails(ctx context.Context, appID, errorID string, query ErrorDetailsQuery) (ErrorDetails, error) {
	key, err := decodeErrorID(errorID)
	if err != nil {
		return ErrorDetails{}, err
	}
	query.ExplorerQuery, err = normalizeErrorsQuery(query.ExplorerQuery)
	if err != nil {
		return ErrorDetails{}, err
	}
	query.Fatality, err = normalizeErrorFatality(query.Fatality)
	if err != nil {
		return ErrorDetails{}, err
	}
	query.Limit, err = errorsPageLimit(query.Limit)
	if err != nil {
		return ErrorDetails{}, err
	}
	if query.Cursor != nil && (query.Cursor.Timestamp.IsZero() || !isCursorKey(query.Cursor.EventKey)) {
		return ErrorDetails{}, ErrInvalidErrorsQuery
	}
	details := ErrorDetails{Available: e != nil && e.clickhouse != nil, From: query.From, To: query.To, BucketSeconds: int64(query.Bucket / time.Second), Series: emptyErrorSeries(query.ExplorerQuery), Updates: []ErrorBreakdown{}, DeviceModels: []ErrorBreakdown{}, OSVersions: []ErrorBreakdown{}, Runtimes: []ErrorBreakdown{}, Occurrences: []ObserveLog{}}
	if !details.Available {
		return details, nil
	}
	ctx, cancel := context.WithTimeout(ctx, telemetryReadTimeout)
	defer cancel()
	ctx, resolved, empty, err := e.prepareTelemetryRead(ctx, appID, query.ExplorerQuery)
	if err != nil {
		return ErrorDetails{}, err
	}
	if empty {
		return details, nil
	}
	query.ExplorerQuery = resolved
	key, err = e.resolveErrorID(ctx, appID, key)
	if err != nil {
		return ErrorDetails{}, err
	}
	if err = e.readErrorAggregates(ctx, appID, key, query, &details); err != nil {
		return ErrorDetails{}, err
	}
	if details.Summary == nil {
		return details, nil
	}
	if err = e.readErrorOccurrences(ctx, appID, key, query, &details); err != nil {
		return ErrorDetails{}, err
	}
	if err = e.enrichErrorUpdates(ctx, appID, details.Updates); err != nil {
		return ErrorDetails{}, err
	}
	return details, nil
}

// GROUPING SETS computes the summary, histogram and all four breakdowns from
// the same filtered, deduplicated stream in one pass. Returned groups are
// bounded; omitted breakdown values are reconciled against the full total.
// A publish group is metadata, not part of the update/platform identity:
// older events may lack it even when newer events refer to the same update.
func (e *Explorer) readErrorAggregates(ctx context.Context, appID, key string, query ErrorDetailsQuery, details *ErrorDetails) error {
	source, args := errorsSource(appID, query.ExplorerQuery, query.Fatality, []string{key})
	sql := sqlf(`WITH %s
 SELECT multiIf(grouping(bucket) = 0, 'series', grouping(update_key) = 0, 'updates',
 grouping(device_model) = 0, 'models', grouping(os_key) = 0, 'os', grouping(runtime_version) = 0, 'runtimes', 'summary') AS kind,
 bucket, update_key, device_model, os_key, runtime_version,
 count() AS occurrences, uniqExactIf(eas_client_id, eas_client_id != toUUID('00000000-0000-0000-0000-000000000000')), countIf(is_fatal = 1), min(timestamp), max(timestamp),
 argMax(error_type, timestamp), argMax(message, timestamp), argMax(culprit, timestamp), argMax(symbolication_status, timestamp),
 argMaxIf(toString(update_group_id), timestamp, update_group_id != toUUID('00000000-0000-0000-0000-000000000000'))
 FROM (
 SELECT *, intDiv(toUnixTimestamp64Nano(timestamp) - ?, ?) AS bucket,
 toJSONString([toString(update_id), platform]) AS update_key,
 toJSONString([os_name, os_version]) AS os_key
 FROM errors_source WHERE group_key = ?
 )
 GROUP BY GROUPING SETS ((), (bucket), (update_key), (device_model), (os_key), (runtime_version))
 ORDER BY kind, ((kind = 'models' AND device_model = '') OR (kind = 'runtimes' AND runtime_version = '') OR (kind = 'os' AND os_key = '["",""]')) DESC, occurrences DESC, update_key, device_model, os_key, runtime_version
 LIMIT 121 BY kind`, source)
	args = append(args, query.From.UnixNano(), int64(query.Bucket), key)
	rows, err := e.clickhouse.Conn.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("reading error details: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, updateKey, model, osKey, runtime, status, updateGroup string
		var bucket int64
		var summary ErrorSummary
		if err := rows.Scan(&kind, &bucket, &updateKey, &model, &osKey, &runtime, &summary.Occurrences, &summary.ImpactedDevices, &summary.CrashOccurrences, &summary.FirstSeen, &summary.LastSeen, &summary.ErrorType, &summary.Message, &summary.Culprit, &status, &updateGroup); err != nil {
			return err
		}
		summary.SymbolicationStatus = ErrorGroupStatus(status)
		segment := ErrorBreakdown{Occurrences: summary.Occurrences}
		switch kind {
		case "summary":
			if summary.Occurrences > 0 {
				summary.ErrorID = encodeErrorID(key)
				details.Summary = &summary
			}
		case "series":
			if bucket >= 0 && bucket < int64(len(details.Series)) {
				details.Series[bucket].Count = summary.Occurrences
			}
		case "updates":
			var parts []string
			if err := json.Unmarshal([]byte(updateKey), &parts); err != nil || len(parts) != 2 {
				return fmt.Errorf("invalid update breakdown")
			}
			segment.Key, segment.UpdateID, segment.Label = updateKey, parts[0], parts[0]
			segment.Platform = parts[1]
			segment.UpdateGroupID = updateGroup
			if parts[0] == ZeroUpdateID {
				segment.Label = "Embedded bundle"
			}
			details.Updates = append(details.Updates, segment)
		case "models":
			segment.Key, segment.Label = model, model
			details.DeviceModels = append(details.DeviceModels, segment)
		case "os":
			var parts []string
			if err := json.Unmarshal([]byte(osKey), &parts); err != nil || len(parts) != 2 {
				return fmt.Errorf("invalid OS breakdown")
			}
			segment.Key = osKey
			segment.OSName, segment.OSVersion = parts[0], parts[1]
			segment.Label = parts[0]
			if parts[1] != "" {
				if segment.Label != "" {
					segment.Label += " "
				}
				segment.Label += parts[1]
			}
			if segment.Label == "" {
				segment.Key = ""
			}
			details.OSVersions = append(details.OSVersions, segment)
		case "runtimes":
			segment.Key, segment.Label = runtime, runtime
			details.Runtimes = append(details.Runtimes, segment)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if details.Summary != nil {
		for _, segments := range []*[]ErrorBreakdown{&details.Updates, &details.DeviceModels, &details.OSVersions, &details.Runtimes} {
			*segments = completeErrorBreakdown(*segments, details.Summary.Occurrences)
		}
	}
	return nil
}

func completeErrorBreakdown(segments []ErrorBreakdown, total uint64) []ErrorBreakdown {
	const limit = 50
	if len(segments) > limit {
		segments = segments[:limit]
	}
	var shown uint64
	for i := range segments {
		if segments[i].Label == "" {
			segments[i].Label = "Unknown"
		}
		shown += segments[i].Occurrences
		if total > 0 {
			segments[i].Percentage = 100 * float64(segments[i].Occurrences) / float64(total)
		}
	}
	if shown < total {
		segments = append(segments, ErrorBreakdown{Key: "__other__", Label: "Other", Occurrences: total - shown, Percentage: 100 * float64(total-shown) / float64(total)})
	}
	return segments
}

func (e *Explorer) readErrorOccurrences(ctx context.Context, appID, key string, query ErrorDetailsQuery, details *ErrorDetails) error {
	source, args := errorsSource(appID, query.ExplorerQuery, query.Fatality, []string{key})
	cursor := sqlFragment("")
	selectedArgs := []any{key}
	if query.Cursor != nil {
		cursor = " AND (timestamp < fromUnixTimestamp64Nano(?) OR (timestamp = fromUnixTimestamp64Nano(?) AND event_key < ?))"
		selectedArgs = append(selectedArgs, query.Cursor.Timestamp.UnixNano(), query.Cursor.Timestamp.UnixNano(), query.Cursor.EventKey)
	}
	selectedArgs = append(selectedArgs, query.Limit+1)
	// Only fetch body/attributes for the selected keys. Large stack payloads
	// never enter the aggregation for every occurrence in the 31-day window.
	where, payloadArgs := errorsTelemetryWhere(query.ExplorerQuery)
	sql := sqlf(`WITH %s,
 selected_occurrences AS (
 SELECT event_key FROM errors_source WHERE group_key = ? %s
 ORDER BY timestamp DESC, event_key DESC LIMIT ?
 ),
 payloads AS (
 SELECT toString(l.content_key) AS event_key,
 argMax(tuple(timestamp, toString(eas_client_id), toString(update_id), branch, channel,
 runtime_version, platform, toString(session_id), event_name, severity_number, severity_text,
 toUInt8(is_fatal = 1 OR event_name = 'xprem_js_crash'), body, attributes, os_name, os_version,
 device_model, country_code, app_version, app_build_number, toString(eas_build_id), environment,
 sdk_version, toString(error_fingerprint)), ingested_at) AS record
 FROM observe_logs l WHERE %s AND toString(l.content_key) IN (SELECT event_key FROM selected_occurrences)
 GROUP BY event_key
 )
 SELECT event_key, record.1, record.2, record.3, record.4, record.5, record.6, record.7,
 record.8, record.9, record.10, record.11, record.12, record.13, record.14, record.15,
 record.16, record.17, record.18, record.19, record.20, record.21, record.22, record.23, record.24
 FROM payloads ORDER BY record.1 DESC, event_key DESC`, source, cursor, where)
	args = append(args, selectedArgs...)
	args = append(args, prependAppID(appID, payloadArgs)...)
	rows, err := e.clickhouse.Conn.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("reading error occurrences: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row ObserveLog
		var fatal uint8
		if err := rows.Scan(&row.EventKey, &row.Timestamp, &row.EASClientID, &row.UpdateID, &row.Branch, &row.Channel, &row.RuntimeVersion, &row.Platform, &row.SessionID, &row.EventName, &row.SeverityNumber, &row.SeverityText, &fatal, &row.Body, &row.Attributes, &row.OSName, &row.OSVersion, &row.DeviceModel, &row.CountryCode, &row.AppVersion, &row.AppBuildNumber, &row.EASBuildID, &row.Environment, &row.SDKVersion, &row.ErrorFingerprint); err != nil {
			return err
		}
		row.IsFatal = fatal == 1
		if row.ErrorFingerprint == ZeroUpdateID {
			row.ErrorFingerprint = ""
		}
		details.Occurrences = append(details.Occurrences, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(details.Occurrences) > query.Limit {
		details.Occurrences = details.Occurrences[:query.Limit]
		last := details.Occurrences[len(details.Occurrences)-1]
		details.NextCursor = EncodeLogCursor(LogCursor{Timestamp: last.Timestamp, EventKey: last.EventKey})
	}
	if len(details.Occurrences) > 0 {
		row := details.Occurrences[0]
		details.RepresentativeOccurrence = &row
	}
	return nil
}

// One bounded PostgreSQL lookup enriches existing updates. Deleted releases
// retain the exact update UUID from the event as a useful, filterable label.
func (e *Explorer) enrichErrorUpdates(ctx context.Context, appID string, segments []ErrorBreakdown) error {
	if e.postgres == nil || e.postgres.DB == nil {
		return nil
	}
	ids := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment.UpdateID != "" && segment.UpdateID != ZeroUpdateID {
			ids = append(ids, segment.UpdateID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := e.postgres.DB.Query(ctx, `SELECT u.update_uuid::text, coalesce(u.message,''), coalesce(u.publish_group::text,'')
 FROM updates u JOIN branches b ON b.id=u.branch_id
 WHERE b.app_id=$1 AND u.update_uuid=ANY($2::uuid[])`, appID, ids)
	if err != nil {
		return fmt.Errorf("reading error update labels: %w", err)
	}
	defer rows.Close()
	type label struct{ message, group string }
	labels := make(map[string]label, len(ids))
	for rows.Next() {
		var id string
		var value label
		if err := rows.Scan(&id, &value.message, &value.group); err != nil {
			return err
		}
		labels[id] = value
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range segments {
		if value, ok := labels[segments[i].UpdateID]; ok {
			if value.message != "" {
				segments[i].Label = value.message
			}
			if value.group != "" && value.group != ZeroUpdateID {
				segments[i].UpdateGroupID = value.group
			}
		}
	}
	return nil
}
