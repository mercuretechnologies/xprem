// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"fmt"
	"time"
)

// UpdateError is one distinct error of an update, and how often it happened.
type UpdateError struct {
	Fingerprint string    `json:"fingerprint"`
	Title       string    `json:"title"`
	Occurrences uint64    `json:"occurrences"`
	Crashes     uint64    `json:"crashes"`
	Devices     uint64    `json:"devices"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
}

type UpdateErrors struct {
	Available bool          `json:"available"`
	Errors    []UpdateError `json:"errors"`
}

// maxUpdateErrors bounds one read: an update with more distinct errors shows
// its most frequent ones.
const maxUpdateErrors = 200

// ReadUpdateErrors lists up to 200 errors of an update, most frequent first.
// Without ClickHouse it returns Available=false and an empty list; query
// and row-reading errors otherwise propagate.
func (e *Explorer) ReadUpdateErrors(ctx context.Context, appID, updateID string) (UpdateErrors, error) {
	result := UpdateErrors{Available: e.clickhouse != nil, Errors: []UpdateError{}}
	if e.clickhouse == nil {
		return result, nil
	}
	rows, err := e.clickhouse.Conn.Query(ctx, `
		SELECT toString(error_fingerprint), any(title), sum(occurrences), sum(crashes),
		       uniqMerge(devices), min(first_seen), max(last_seen)
		FROM error_occurrences
		WHERE app_id = ? AND update_id = ?
		GROUP BY error_fingerprint
		ORDER BY sum(occurrences) DESC
		LIMIT ?`, appID, updateID, maxUpdateErrors)
	if err != nil {
		return UpdateErrors{}, fmt.Errorf("reading the errors of update %s: %w", updateID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var updateError UpdateError
		if err := rows.Scan(
			&updateError.Fingerprint, &updateError.Title, &updateError.Occurrences, &updateError.Crashes,
			&updateError.Devices, &updateError.FirstSeen, &updateError.LastSeen,
		); err != nil {
			return UpdateErrors{}, err
		}
		result.Errors = append(result.Errors, updateError)
	}
	return result, rows.Err()
}
