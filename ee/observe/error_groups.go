// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"xprem/ee/symbolication"
)

// ErrorGroup is what an error of an update is, once one of its traces went
// through the update's source map.
type ErrorGroup struct {
	Fingerprint string `json:"fingerprint"`
	// GroupFingerprint is the same for this error in every update;
	// noGroupFingerprint when it has none.
	GroupFingerprint string              `json:"groupFingerprint"`
	ErrorType        string              `json:"errorType"`
	Message          string              `json:"message"`
	Culprit          string              `json:"culprit"`
	Trace            symbolication.Trace `json:"trace"`
	SymbolicatedAt   time.Time           `json:"symbolicatedAt"`
}

// errorKey names one error of one update.
type errorKey struct {
	appID       string
	updateID    string
	fingerprint string
}

// groupedError is a group with the error it belongs to.
type groupedError struct {
	errorKey
	ErrorGroup
}

// noGroupFingerprint is the group of an error whose update has no source map.
const noGroupFingerprint = ZeroUpdateID

// ReadErrorGroup answers nil when the error has no group.
func (e *Explorer) ReadErrorGroup(ctx context.Context, appID, updateID, fingerprint string) (*ErrorGroup, error) {
	if e.clickhouse == nil {
		return nil, nil
	}
	rows, err := e.clickhouse.Conn.Query(ctx, `
		SELECT toString(error_fingerprint), toString(group_fingerprint), error_type, message, culprit, trace, symbolicated_at
		FROM error_groups FINAL
		WHERE app_id = ? AND update_id = ? AND error_fingerprint = ?`, appID, updateID, fingerprint)
	if err != nil {
		return nil, fmt.Errorf("reading the group of error %s: %w", fingerprint, err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var group ErrorGroup
	var trace string
	if err := rows.Scan(&group.Fingerprint, &group.GroupFingerprint, &group.ErrorType, &group.Message,
		&group.Culprit, &trace, &group.SymbolicatedAt); err != nil {
		return nil, err
	}
	if group.GroupFingerprint == noGroupFingerprint {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(trace), &group.Trace); err != nil {
		return nil, fmt.Errorf("reading the trace of error %s: %w", fingerprint, err)
	}
	return &group, nil
}

// pendingErrorGroups lists the errors counted lately that have neither a
// group nor a mark, most frequent first, from the offset-th one.
func (e *Explorer) pendingErrorGroups(ctx context.Context, since time.Time, limit, offset int) ([]errorKey, error) {
	rows, err := e.clickhouse.Conn.Query(ctx, `
		SELECT toString(app_id), toString(update_id), toString(error_fingerprint)
		FROM error_occurrences
		WHERE hour >= ?
		  AND (app_id, update_id, error_fingerprint) NOT IN (
		      SELECT app_id, update_id, error_fingerprint FROM error_groups
		      WHERE (app_id, update_id) IN (
		          SELECT app_id, update_id FROM error_occurrences WHERE hour >= ?))
		GROUP BY app_id, update_id, error_fingerprint
		ORDER BY sum(occurrences) DESC, app_id, update_id, error_fingerprint
		LIMIT ? OFFSET ?`, since, since, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing the errors without a group: %w", err)
	}
	defer rows.Close()
	var pending []errorKey
	for rows.Next() {
		var key errorKey
		if err := rows.Scan(&key.appID, &key.updateID, &key.fingerprint); err != nil {
			return nil, err
		}
		pending = append(pending, key)
	}
	return pending, rows.Err()
}

// oneTraceOf reads the exception of the latest occurrence of an error; every
// occurrence has the same trace, which is what the fingerprint says.
func (e *Explorer) oneTraceOf(ctx context.Context, key errorKey) (exception, error) {
	var eventName, body, attributes string
	err := e.clickhouse.Conn.QueryRow(ctx, `
		SELECT event_name, body, attributes FROM observe_logs
		WHERE app_id = ? AND update_id = ? AND error_fingerprint = ?
		ORDER BY timestamp DESC
		LIMIT 1`, key.appID, key.updateID, key.fingerprint).Scan(&eventName, &body, &attributes)
	if err != nil {
		return exception{}, fmt.Errorf("reading a trace of error %s: %w", key.fingerprint, err)
	}
	parsed := map[string]any{}
	if attributes != "" {
		if err := json.Unmarshal([]byte(attributes), &parsed); err != nil {
			return exception{}, fmt.Errorf("reading the attributes of error %s: %w", key.fingerprint, err)
		}
	}
	return exceptionOf(eventName, body, parsed), nil
}

func (e *Explorer) writeErrorGroups(ctx context.Context, groups []groupedError) error {
	if len(groups) == 0 {
		return nil
	}
	batch, err := e.clickhouse.Conn.PrepareBatch(ctx, `INSERT INTO error_groups
		(app_id, update_id, error_fingerprint, group_fingerprint, error_type, message, culprit, trace, symbolicated_at)`)
	if err != nil {
		return err
	}
	defer batch.Close()
	for _, group := range groups {
		trace, err := json.Marshal(group.Trace)
		if err != nil {
			return err
		}
		if err := batch.Append(group.appID, group.updateID, group.fingerprint, group.GroupFingerprint,
			group.ErrorType, group.Message, group.Culprit, string(trace), group.SymbolicatedAt); err != nil {
			return err
		}
	}
	return batch.Send()
}
