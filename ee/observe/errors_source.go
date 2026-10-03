// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Preserve fallback keys so bookmarks survive later symbolication.
func encodeErrorID(key string) string { return base64.RawURLEncoding.EncodeToString([]byte(key)) }
func decodeErrorID(id string) (string, error) {
	if len(id) > 8192 {
		return "", ErrInvalidErrorID
	}
	decoded, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", ErrInvalidErrorID
	}
	key := string(decoded)
	if strings.HasPrefix(key, "g:") {
		id, err := uuid.Parse(strings.TrimPrefix(key, "g:"))
		if err != nil || id == uuid.Nil {
			return "", ErrInvalidErrorID
		}
		return "g:" + id.String(), nil
	}
	if !strings.HasPrefix(key, "f:") {
		return "", ErrInvalidErrorID
	}
	var parts []string
	if err := json.Unmarshal([]byte(key[2:]), &parts); err != nil || len(parts) != 8 {
		return "", ErrInvalidErrorID
	}
	if _, err := uuid.Parse(parts[0]); err != nil || parts[1] == "" {
		return "", ErrInvalidErrorID
	}
	return key, nil
}

// ClickHouse expands CTEs, so this scans filtered logs twice.
func errorsSource(appID string, query ExplorerQuery, fatality string, keys []string, snapshot ...time.Time) (sqlFragment, []any) {
	var where sqlFragment
	var args []any
	if len(snapshot) > 0 {
		where, args = errorsSnapshotWhere(snapshot[0])
	} else {
		where, args = errorsTelemetryWhere(query)
	}
	where += ` AND (l.error_fingerprint != toUUID('00000000-0000-0000-0000-000000000000')
 OR l.severity_number >= 17 OR l.is_fatal = 1 OR l.event_name = 'xprem_js_crash')`
	switch fatality {
	case "fatal":
		where += " AND (l.is_fatal = 1 OR l.event_name = 'xprem_js_crash')"
	case "nonfatal":
		where += " AND l.is_fatal = 0 AND l.event_name != 'xprem_js_crash'"
	}
	scoped, scopedArgs := errorsScope(appID, keys)
	where += scoped
	args = append(args, scopedArgs...)

	columns := []sqlFragment{
		"timestamp", "eas_client_id", "update_id", "error_fingerprint", "update_group_id",
		"platform", "runtime_version", "app_version", "app_build_number", "eas_build_id",
		"device_model", "os_name", "os_version",
		"toUInt8(is_fatal = 1 OR event_name = 'xprem_js_crash')",
		"coalesce(nullIf(JSONExtractString(attributes, 'exception.type'), ''), JSONExtractString(attributes, 'name'))",
		"coalesce(nullIf(JSONExtractString(attributes, 'exception.message'), ''), nullIf(JSONExtractString(attributes, 'message'), ''), nullIf(body, ''), event_name)",
		// Older records have no fingerprint; hash their error content.
		"if(error_fingerprint = toUUID('00000000-0000-0000-0000-000000000000'), hex(SHA256(concat(event_name, '\\0', body, '\\0', attributes))), toString(error_fingerprint))",
	}
	names := []sqlFragment{"timestamp", "eas_client_id", "update_id", "error_fingerprint", "update_group_id", "platform", "runtime_version", "app_version", "app_build_number", "eas_build_id", "device_model", "os_name", "os_version", "is_fatal", "raw_error_type", "raw_message", "raw_identity"}
	projections := make([]sqlFragment, 0, len(names))
	for i, name := range names {
		projections = append(projections, sqlFragment(fmt.Sprintf("record.%d AS %s", i+1, name)))
	}
	// Embedded bundles lack an update ID, so include their build context.
	fallback := sqlFragment(`concat('f:', toJSONString([toString(d.update_id), d.raw_identity,
 if(d.update_id = toUUID('00000000-0000-0000-0000-000000000000'), d.platform, ''),
 if(d.update_id = toUUID('00000000-0000-0000-0000-000000000000'), d.runtime_version, ''),
 if(d.update_id = toUUID('00000000-0000-0000-0000-000000000000'), d.app_version, ''),
 if(d.update_id = toUUID('00000000-0000-0000-0000-000000000000'), d.app_build_number, ''),
 if(d.update_id = toUUID('00000000-0000-0000-0000-000000000000'), toString(d.eas_build_id), ''), 'v1']))`)
	source := sqlFragment(sqlf(`
 error_logs AS (
 SELECT toString(l.content_key) AS event_key, argMax(tuple(%s), l.ingested_at) AS record
 FROM observe_logs l WHERE %s GROUP BY event_key
 ),
 deduplicated_errors AS (SELECT event_key, %s FROM error_logs),
 latest_error_groups AS (
 SELECT update_id, error_fingerprint,
 argMax(tuple(group_fingerprint, error_type, message, culprit), tuple(symbolicated_at, group_fingerprint != toUUID('00000000-0000-0000-0000-000000000000'))) AS metadata,
 max(symbolicated_at) AS latest_symbolicated_at
 FROM error_groups WHERE app_id = ? AND (update_id, error_fingerprint) IN (
 SELECT l.update_id, l.error_fingerprint FROM observe_logs l WHERE %s
 AND l.error_fingerprint != toUUID('00000000-0000-0000-0000-000000000000')
 ) GROUP BY update_id, error_fingerprint
 ),
 errors_source AS (
 SELECT d.*, if(g.metadata.1 != toUUID('00000000-0000-0000-0000-000000000000'), concat('g:', toString(g.metadata.1)), %s) AS group_key,
 if(g.metadata.2 != '', g.metadata.2, d.raw_error_type) AS error_type,
 if(g.metadata.3 != '', g.metadata.3, d.raw_message) AS message,
 g.metadata.4 AS culprit,
 multiIf(g.metadata.1 != toUUID('00000000-0000-0000-0000-000000000000'), 'ready',
 g.latest_symbolicated_at > toDateTime(0), 'no_sourcemap', 'waiting') AS symbolication_status
 FROM deduplicated_errors d LEFT JOIN latest_error_groups g
 ON d.update_id = g.update_id AND d.error_fingerprint = g.error_fingerprint
 )`, joinFragments(columns, ", "), where, joinFragments(projections, ", "), where, fallback))
	allArgs := prependAppID(appID, args)
	allArgs = append(allArgs, appID)
	allArgs = append(allArgs, prependAppID(appID, args)...)
	return source, allArgs
}

func (e *Explorer) resolveErrorID(ctx context.Context, appID, key string) (string, error) {
	if strings.HasPrefix(key, "g:") {
		return key, nil
	}
	var parts []string
	if err := json.Unmarshal([]byte(key[2:]), &parts); err != nil {
		return "", ErrInvalidErrorID
	}
	fingerprint, err := uuid.Parse(parts[1])
	if err != nil || fingerprint == uuid.Nil {
		return key, nil
	}
	// Resolve the original fingerprint even when filters exclude its update.
	var group string
	err = e.clickhouse.Conn.QueryRow(ctx, `SELECT toString(argMax(group_fingerprint,
 tuple(symbolicated_at, group_fingerprint != toUUID('00000000-0000-0000-0000-000000000000'))))
 FROM error_groups WHERE app_id = ? AND update_id = ? AND error_fingerprint = ?`, appID, parts[0], parts[1]).Scan(&group)
	if err != nil {
		return "", fmt.Errorf("resolving error reference: %w", err)
	}
	if group != "" && group != ZeroUpdateID {
		return "g:" + group, nil
	}
	return key, nil
}

// Stale mappings may admit extra keys; the latest mapping decides membership.
func errorsScope(appID string, keys []string) (sqlFragment, []any) {
	if len(keys) == 0 {
		return "", nil
	}
	var groups []string
	var alternatives []sqlFragment
	var args []any
	for _, key := range keys {
		if strings.HasPrefix(key, "g:") {
			groups = append(groups, key[2:])
			continue
		}
		var parts []string
		if json.Unmarshal([]byte(key[2:]), &parts) != nil || len(parts) != 8 {
			continue
		}
		clause := sqlFragment("(l.update_id = ?")
		args = append(args, parts[0])
		fingerprint, err := uuid.Parse(parts[1])
		if err != nil || fingerprint == uuid.Nil {
			clause += " AND l.error_fingerprint = toUUID('00000000-0000-0000-0000-000000000000')"
		} else {
			clause += " AND l.error_fingerprint = ?"
			args = append(args, parts[1])
		}
		if parts[0] == ZeroUpdateID {
			clause += " AND l.platform = ? AND l.runtime_version = ? AND l.app_version = ? AND l.app_build_number = ? AND l.eas_build_id = ?"
			args = append(args, parts[2], parts[3], parts[4], parts[5], parts[6])
		}
		alternatives = append(alternatives, clause+")")
	}
	if len(groups) > 0 {
		alternatives = append(alternatives, "(l.update_id, l.error_fingerprint) IN (SELECT update_id, error_fingerprint FROM error_groups WHERE app_id = ? AND group_fingerprint IN ?)")
		args = append(args, appID, groups)
	}
	if len(alternatives) == 0 {
		return " AND 0", nil
	}
	return " AND (" + joinFragments(alternatives, " OR ") + ")", args
}

func errorsTelemetryWhere(query ExplorerQuery) (sqlFragment, []any) {
	return telemetryWhereNanoseconds("l", query, len(query.MetadataFilter) > 0)
}

// Ingestion has second precision; exclude the current second to stabilize paging.
func errorsSnapshotWhere(asOf time.Time) (sqlFragment, []any) {
	return "l.app_id = ? AND l.ingested_at < fromUnixTimestamp64Nano(?)", []any{asOf.UnixNano()}
}
