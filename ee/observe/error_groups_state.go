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

// Progress and retry lists stay bounded even when jobs run more often than
// their minute interval. Eviction permits repeated work, never drops errors.
const (
	errorGroupsMaxDeferredUpdates = 1000
	errorGroupsMaxAppCursors      = 1000
)

type errorGroupsCursor struct {
	AppID       string `json:"appId"`
	UpdateID    string `json:"updateId"`
	Fingerprint string `json:"fingerprint"`
}

type errorGroupsSweepState struct {
	LastApp  string                          `json:"lastApp,omitempty"`
	Cursors  map[string]errorGroupsAppCursor `json:"cursors,omitempty"`
	Deferred map[string]time.Time            `json:"deferred,omitempty"`
}

type errorGroupsAppCursor struct {
	UpdateID    string    `json:"updateId"`
	Fingerprint string    `json:"fingerprint"`
	TouchedAt   time.Time `json:"touchedAt"`
}

func (s *errorGroupsSweepState) prune(now time.Time) {
	for key, until := range s.Deferred {
		if !until.After(now) {
			delete(s.Deferred, key)
		}
	}
	for app, cursor := range s.Cursors {
		if !cursor.TouchedAt.After(now.Add(-errorGroupsLookback)) {
			delete(s.Cursors, app)
		}
	}
}

func (s *errorGroupsSweepState) cursor() *errorGroupsCursor {
	cursor, exists := s.Cursors[s.LastApp]
	if !exists {
		return nil
	}
	return &errorGroupsCursor{AppID: s.LastApp, UpdateID: cursor.UpdateID, Fingerprint: cursor.Fingerprint}
}

func (s *errorGroupsSweepState) advance(key errorKey, now time.Time) {
	if s.Cursors == nil {
		s.Cursors = make(map[string]errorGroupsAppCursor)
	}
	if _, exists := s.Cursors[key.appID]; !exists && len(s.Cursors) == errorGroupsMaxAppCursors {
		var oldest string
		for app, cursor := range s.Cursors {
			if oldest == "" || cursor.TouchedAt.Before(s.Cursors[oldest].TouchedAt) ||
				(cursor.TouchedAt.Equal(s.Cursors[oldest].TouchedAt) && app < oldest) {
				oldest = app
			}
		}
		delete(s.Cursors, oldest)
	}
	s.Cursors[key.appID] = errorGroupsAppCursor{UpdateID: key.updateID, Fingerprint: key.fingerprint, TouchedAt: now}
}

func (s *errorGroupsSweepState) deferUpdate(key errorKey, until time.Time) {
	if s.Deferred == nil {
		s.Deferred = make(map[string]time.Time)
	}
	update := key.appID + "/" + key.updateID
	if _, exists := s.Deferred[update]; !exists && len(s.Deferred) == errorGroupsMaxDeferredUpdates {
		var oldest string
		for candidate, deadline := range s.Deferred {
			if oldest == "" || deadline.Before(s.Deferred[oldest]) || (deadline.Equal(s.Deferred[oldest]) && candidate < oldest) {
				oldest = candidate
			}
		}
		delete(s.Deferred, oldest)
	}
	s.Deferred[update] = until
}

func (e *Explorer) errorGroupsState(ctx context.Context) (errorGroupsSweepState, error) {
	raw, err := e.postgres.GetObserveErrorGroupSweepState(ctx)
	if err != nil {
		return errorGroupsSweepState{}, fmt.Errorf("reading the error group sweep progress: %w", err)
	}
	var state errorGroupsSweepState
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("decoding the error group sweep progress: %w", err)
	}
	return state, nil
}

func (e *Explorer) saveErrorGroupsState(ctx context.Context, state errorGroupsSweepState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := e.postgres.SaveObserveErrorGroupSweepState(ctx, raw); err != nil {
		return fmt.Errorf("saving the error group sweep progress: %w", err)
	}
	return nil
}
