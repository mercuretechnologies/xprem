// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type UpdateErrorSummary struct {
	ErrorSummary
	// New is unknown until symbolication supplies a group shared across updates.
	New *bool `json:"new"`
}

type UpdateErrorsPage struct {
	Available bool                 `json:"available"`
	UpdateID  string               `json:"updateId"`
	Limit     int                  `json:"limit"`
	Offset    int                  `json:"offset"`
	HasMore   bool                 `json:"hasMore"`
	Errors    []UpdateErrorSummary `json:"errors"`
}

func normalizeUpdateErrorsQuery(appID, updateID string, limit, offset int) (string, string, int, error) {
	app, appErr := uuid.Parse(appID)
	update, updateErr := uuid.Parse(updateID)
	if appErr != nil || updateErr != nil || app == uuid.Nil || update == uuid.Nil || limit < 0 || limit > maxErrorsPage || offset < 0 || offset > 10000 {
		return "", "", 0, ErrInvalidErrorsQuery
	}
	if limit == 0 {
		limit = 25
	}
	return app.String(), update.String(), limit, nil
}

func (e *Explorer) ReadUpdateErrors(ctx context.Context, appID, updateID string, limit, offset int) (UpdateErrorsPage, error) {
	appID, updateID, limit, err := normalizeUpdateErrorsQuery(appID, updateID, limit, offset)
	if err != nil {
		return UpdateErrorsPage{}, err
	}
	return cachedRead(ctx, readCacheKey("update-errors", appID, updateID, limit, offset), func(ctx context.Context) (UpdateErrorsPage, error) {
		return e.readUpdateErrors(ctx, appID, updateID, limit, offset)
	})
}

// Occurrences, fatal crashes, and devices come from the aggregate states.
// Manual crashes without is_fatal were omitted by the materialized view, so
// add a scoped correction from retained logs, deduplicated like the Errors API.
// Aggregate counts still include SDK retries; devices use uniq's approximation.
const updateErrorsSummarySQL = `WITH latest_error_groups AS (
 SELECT error_fingerprint,
 argMax(tuple(group_fingerprint, error_type, message, culprit),
 tuple(symbolicated_at, group_fingerprint != toUUID('00000000-0000-0000-0000-000000000000'))) AS metadata,
 max(symbolicated_at) AS latest_symbolicated_at
 FROM error_groups WHERE app_id = ? AND update_id = ? GROUP BY error_fingerprint
 ), update_error_counts AS (
 SELECT error_fingerprint, argMax(o.title, o.last_seen) AS title, sum(o.occurrences) AS occurrences,
 sum(o.crashes) AS crashes, uniqMergeState(o.devices) AS devices,
 min(o.first_seen) AS first_seen, max(o.last_seen) AS last_seen
 FROM error_occurrences o WHERE app_id = ? AND update_id = ? GROUP BY error_fingerprint
 ), manual_crashes AS (
 SELECT error_fingerprint, uniqExact(content_key) AS crashes
 FROM observe_logs WHERE app_id = ? AND update_id = ?
 AND event_name = 'xprem_js_crash' AND is_fatal = 0
 AND error_fingerprint != toUUID('00000000-0000-0000-0000-000000000000')
 GROUP BY error_fingerprint
 ), update_errors_source AS (
 SELECT o.*, o.crashes + m.crashes AS crash_count,
 if(g.metadata.1 != toUUID('00000000-0000-0000-0000-000000000000'),
 concat('g:', toString(g.metadata.1)),
 concat('f:', toJSONString([?, toString(o.error_fingerprint), '', '', '', '', '', 'v1']))) AS group_key,
 g.metadata.2 AS error_type,
 if(g.metadata.3 != '', g.metadata.3, o.title) AS message,
 g.metadata.4 AS culprit,
 multiIf(g.metadata.1 != toUUID('00000000-0000-0000-0000-000000000000'), 'ready',
 g.latest_symbolicated_at > toDateTime(0), 'no_sourcemap', 'waiting') AS symbolication_status
 FROM update_error_counts o LEFT JOIN latest_error_groups g ON o.error_fingerprint = g.error_fingerprint
 LEFT JOIN manual_crashes m ON o.error_fingerprint = m.error_fingerprint
 )
 SELECT group_key, argMax(error_type, last_seen), argMax(message, last_seen),
 argMax(culprit, last_seen), argMax(symbolication_status, last_seen),
 sum(occurrences) AS occurrence_count, uniqMerge(devices), sum(crash_count),
 min(first_seen), max(last_seen) AS last_seen_at
 FROM update_errors_source GROUP BY group_key
 ORDER BY occurrence_count DESC, last_seen_at DESC, group_key LIMIT ? OFFSET ?`

// The bloom-filtered group lookup narrows candidate keys, then ALL versions of
// those keys decide their latest group. Filtering before argMax alone would
// treat an old mapping as a pre-existing error after it moved to another group.
const updateErrorsHistorySQL = `WITH candidate_keys AS (
 SELECT update_id, error_fingerprint FROM error_groups
 WHERE app_id = ? AND update_id != ? AND group_fingerprint IN ?
 GROUP BY update_id, error_fingerprint
 ), latest_error_groups AS (
 SELECT update_id, error_fingerprint,
 argMax(group_fingerprint, tuple(symbolicated_at,
 group_fingerprint != toUUID('00000000-0000-0000-0000-000000000000'))) AS latest_group
 FROM error_groups WHERE app_id = ?
 AND (update_id, error_fingerprint) IN (SELECT update_id, error_fingerprint FROM candidate_keys)
 GROUP BY update_id, error_fingerprint
 ), historical_occurrences AS (
 SELECT update_id, error_fingerprint, min(first_seen) AS first_seen
 FROM error_occurrences WHERE app_id = ?
 AND (update_id, error_fingerprint) IN (SELECT update_id, error_fingerprint FROM candidate_keys)
 GROUP BY update_id, error_fingerprint
 )
 SELECT toString(g.latest_group), min(o.first_seen)
 FROM historical_occurrences o INNER JOIN latest_error_groups g
 ON o.update_id = g.update_id AND o.error_fingerprint = g.error_fingerprint
 WHERE g.latest_group IN ? GROUP BY g.latest_group`

func (e *Explorer) readUpdateErrors(ctx context.Context, appID, updateID string, limit, offset int) (UpdateErrorsPage, error) {
	appID, updateID, limit, err := normalizeUpdateErrorsQuery(appID, updateID, limit, offset)
	if err != nil {
		return UpdateErrorsPage{}, err
	}
	page := UpdateErrorsPage{Available: e != nil && e.clickhouse != nil && e.clickhouse.Conn != nil,
		UpdateID: updateID, Limit: limit, Offset: offset, Errors: []UpdateErrorSummary{}}
	if !page.Available {
		return page, nil
	}
	ctx, cancel := context.WithTimeout(ctx, telemetryReadTimeout)
	defer cancel()
	rows, err := e.clickhouse.Conn.Query(ctx, updateErrorsSummarySQL, appID, updateID, appID, updateID, appID, updateID, updateID, limit+1, offset)
	if err != nil {
		return UpdateErrorsPage{}, fmt.Errorf("reading update errors: %w", err)
	}
	for rows.Next() {
		var summary UpdateErrorSummary
		var key, status string
		if err := rows.Scan(&key, &summary.ErrorType, &summary.Message, &summary.Culprit, &status,
			&summary.Occurrences, &summary.ImpactedDevices, &summary.CrashOccurrences, &summary.FirstSeen, &summary.LastSeen); err != nil {
			rows.Close()
			return UpdateErrorsPage{}, err
		}
		summary.ErrorID = encodeErrorID(key)
		summary.SymbolicationStatus = ErrorGroupStatus(status)
		page.Errors = append(page.Errors, summary)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return UpdateErrorsPage{}, err
	}
	if len(page.Errors) > limit {
		page.HasMore = true
		page.Errors = page.Errors[:limit]
	}
	if err := e.readUpdateErrorsHistory(ctx, appID, updateID, page.Errors); err != nil {
		return UpdateErrorsPage{}, err
	}
	return page, nil
}

func (e *Explorer) readUpdateErrorsHistory(ctx context.Context, appID, updateID string, summaries []UpdateErrorSummary) error {
	groups := make([]string, 0, len(summaries))
	indexes := make(map[string]int, len(summaries))
	for i := range summaries {
		key, err := decodeErrorID(summaries[i].ErrorID)
		if err != nil {
			return err
		}
		if strings.HasPrefix(key, "g:") {
			group := strings.TrimPrefix(key, "g:")
			groups = append(groups, group)
			indexes[group] = i
			isNew := true
			summaries[i].New = &isNew
		}
	}
	if len(groups) == 0 {
		return nil
	}
	rows, err := e.clickhouse.Conn.Query(ctx, updateErrorsHistorySQL, appID, updateID, groups, appID, appID, groups)
	if err != nil {
		return fmt.Errorf("reading update error history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var group string
		var firstSeen time.Time
		if err := rows.Scan(&group, &firstSeen); err != nil {
			return err
		}
		if i, ok := indexes[group]; ok {
			*summaries[i].New = firstSeen.After(summaries[i].FirstSeen)
		}
	}
	return rows.Err()
}
