// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const ErrorsMaxWindow = 31 * 24 * time.Hour
const maxErrorsPage = 100
const maxErrorsBuckets = 120

var ErrInvalidErrorsQuery = errors.New("invalid errors query")
var ErrInvalidErrorID = errors.New("invalid error ID")

type ErrorsQuery struct {
	ExplorerQuery
	Search        string
	Fatality      string
	Sort          string
	Limit         int
	Offset        int
	IncludeSeries bool
}

type ErrorSummary struct {
	ErrorID             string              `json:"errorId"`
	ErrorType           string              `json:"errorType"`
	Message             string              `json:"message"`
	Culprit             string              `json:"culprit"`
	SymbolicationStatus ErrorGroupStatus    `json:"symbolicationStatus"`
	Occurrences         uint64              `json:"occurrences"`
	ImpactedDevices     uint64              `json:"impactedDevices"`
	CrashOccurrences    uint64              `json:"crashOccurrences"`
	FirstSeen           time.Time           `json:"firstSeen"`
	LastSeen            time.Time           `json:"lastSeen"`
	Series              []ObserveEventPoint `json:"series,omitempty"`
}

type ErrorsPage struct {
	Available     bool           `json:"available"`
	From          time.Time      `json:"from"`
	To            time.Time      `json:"to"`
	BucketSeconds int64          `json:"bucketSeconds"`
	Limit         int            `json:"limit"`
	Offset        int            `json:"offset"`
	HasMore       bool           `json:"hasMore"`
	Errors        []ErrorSummary `json:"errors"`
}

// All surfaces validate the window here, including callers outside HTTP/MCP.
// Anchoring buckets to From retains both partial edge buckets and caps the
// response size independently of the caller's requested bucket duration.
func normalizeErrorsQuery(query ExplorerQuery) (ExplorerQuery, error) {
	if len(query.Conditions) > 0 {
		return query, fmt.Errorf("%w: conditions are only supported on timing reads", ErrInvalidErrorsQuery)
	}
	if query.From.IsZero() || query.To.IsZero() || !query.To.After(query.From) || query.To.Sub(query.From) > ErrorsMaxWindow {
		return query, fmt.Errorf("%w: the period must be between zero and 31 days", ErrInvalidErrorsQuery)
	}
	query.From, query.To = query.From.UTC(), query.To.UTC()
	minBucket := (query.To.Sub(query.From) / maxErrorsBuckets).Truncate(time.Second) + time.Second
	if query.Bucket < minBucket {
		query.Bucket = minBucket
	}
	query.Bucket = max(query.Bucket.Truncate(time.Second), time.Second)
	return query, nil
}

func normalizeErrorFatality(value string) (string, error) {
	switch value {
	case "", "all":
		return "", nil
	case "fatal":
		return "fatal", nil
	case "nonfatal", "non_fatal":
		return "nonfatal", nil
	default:
		return "", fmt.Errorf("%w: unknown fatality", ErrInvalidErrorsQuery)
	}
}

func errorsPageLimit(limit int) (int, error) {
	if limit == 0 {
		return 50, nil
	}
	if limit < 1 || limit > maxErrorsPage {
		return 0, fmt.Errorf("%w: limit must be between 1 and 100", ErrInvalidErrorsQuery)
	}
	return limit, nil
}

func (e *Explorer) ReadErrors(ctx context.Context, appID string, query ErrorsQuery) (ErrorsPage, error) {
	return cachedRead(ctx, readCacheKey("errors", appID, query), func(ctx context.Context) (ErrorsPage, error) { return e.readErrors(ctx, appID, query) })
}

func (e *Explorer) readErrors(ctx context.Context, appID string, query ErrorsQuery) (ErrorsPage, error) {
	var err error
	query.ExplorerQuery, err = normalizeErrorsQuery(query.ExplorerQuery)
	if err != nil {
		return ErrorsPage{}, err
	}
	query.Fatality, err = normalizeErrorFatality(query.Fatality)
	if err != nil {
		return ErrorsPage{}, err
	}
	query.Limit, err = errorsPageLimit(query.Limit)
	if err != nil {
		return ErrorsPage{}, err
	}
	order := sqlFragment("occurrences DESC, last_seen DESC, group_key")
	switch query.Sort {
	case "", "occurrences":
	case "impactedDevices":
		order = "impacted_devices DESC, last_seen DESC, group_key"
	case "lastSeen":
		order = "last_seen DESC, group_key"
	default:
		return ErrorsPage{}, fmt.Errorf("%w: unknown sort", ErrInvalidErrorsQuery)
	}
	if query.Offset < 0 || query.Offset > 10000 || len(query.Search) > 256 {
		return ErrorsPage{}, ErrInvalidErrorsQuery
	}
	page := ErrorsPage{Available: e != nil && e.clickhouse != nil, From: query.From, To: query.To, BucketSeconds: int64(query.Bucket / time.Second), Limit: query.Limit, Offset: query.Offset, Errors: []ErrorSummary{}}
	if !page.Available {
		return page, nil
	}
	ctx, cancel := context.WithTimeout(ctx, telemetryReadTimeout)
	defer cancel()
	ctx, resolved, empty, err := e.prepareTelemetryRead(ctx, appID, query.ExplorerQuery)
	if err != nil {
		return ErrorsPage{}, err
	}
	if empty {
		return page, nil
	}
	query.ExplorerQuery = resolved
	source, args := errorsSource(appID, query.ExplorerQuery, query.Fatality, nil)
	predicate := sqlFragment("")
	if query.Search != "" {
		predicate = "HAVING countIf(positionCaseInsensitiveUTF8(concat(error_type, ' ', message, ' ', culprit), ?) > 0) > 0"
		args = append(args, query.Search)
	}
	sql := sqlf(`WITH %s
 SELECT group_key, argMax(error_type, timestamp), argMax(message, timestamp),
 argMax(culprit, timestamp), argMax(symbolication_status, timestamp), count() AS occurrences,
 uniqExactIf(eas_client_id, eas_client_id != toUUID('00000000-0000-0000-0000-000000000000')) AS impacted_devices, countIf(is_fatal = 1), min(timestamp), max(timestamp) AS last_seen
 FROM errors_source GROUP BY group_key %s ORDER BY %s LIMIT ? OFFSET ?`, source, predicate, order)
	args = append(args, query.Limit+1, query.Offset)
	rows, err := e.clickhouse.Conn.Query(ctx, sql, args...)
	if err != nil {
		return ErrorsPage{}, fmt.Errorf("reading errors: %w", err)
	}
	for rows.Next() {
		var row ErrorSummary
		var key, status string
		if err := rows.Scan(&key, &row.ErrorType, &row.Message, &row.Culprit, &status, &row.Occurrences, &row.ImpactedDevices, &row.CrashOccurrences, &row.FirstSeen, &row.LastSeen); err != nil {
			rows.Close()
			return ErrorsPage{}, err
		}
		row.SymbolicationStatus = ErrorGroupStatus(status)
		row.ErrorID = encodeErrorID(key)
		page.Errors = append(page.Errors, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ErrorsPage{}, err
	}
	if len(page.Errors) > query.Limit {
		page.HasMore = true
		page.Errors = page.Errors[:query.Limit]
	}
	if query.IncludeSeries && len(page.Errors) > 0 {
		if err := e.readErrorsSeries(ctx, appID, query, page.Errors); err != nil {
			return ErrorsPage{}, err
		}
	}
	return page, nil
}

func (e *Explorer) readErrorsSeries(ctx context.Context, appID string, query ErrorsQuery, summaries []ErrorSummary) error {
	keys := make([]string, 0, len(summaries))
	indexes := make(map[string]int, len(summaries))
	for i := range summaries {
		key, err := decodeErrorID(summaries[i].ErrorID)
		if err != nil {
			return err
		}
		keys = append(keys, key)
		indexes[key] = i
		summaries[i].Series = emptyErrorSeries(query.ExplorerQuery)
	}
	source, args := errorsSource(appID, query.ExplorerQuery, query.Fatality, keys)
	sql := sqlf(`WITH %s
 SELECT group_key, intDiv(toUnixTimestamp64Nano(timestamp) - ?, ?) AS bucket, count()
 FROM errors_source WHERE group_key IN ? GROUP BY group_key, bucket`, source)
	args = append(args, query.From.UnixNano(), int64(query.Bucket), keys)
	rows, err := e.clickhouse.Conn.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("reading error series: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var bucket int64
		var count uint64
		if err := rows.Scan(&key, &bucket, &count); err != nil {
			return err
		}
		if i, ok := indexes[key]; ok && bucket >= 0 && bucket < int64(len(summaries[i].Series)) {
			summaries[i].Series[bucket].Count = count
		}
	}
	return rows.Err()
}

func emptyErrorSeries(query ExplorerQuery) []ObserveEventPoint {
	count := int(query.To.Sub(query.From)/query.Bucket) + 1
	points := make([]ObserveEventPoint, count)
	for i := range points {
		points[i].Timestamp = query.From.Add(time.Duration(i) * query.Bucket)
	}
	return points
}
