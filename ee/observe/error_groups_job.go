// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"errors"
	"log"
	"time"
	"xprem/ee/symbolication"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// IndexOpener opens the index of the source map of an update.
type IndexOpener interface {
	OpenUpdateIndex(ctx context.Context, appID, updateUUID string) (*symbolication.Index, error)
}

// The sweep's pace: how often it runs, how many errors one run takes, how far
// back it looks for them, and how long it may take.
const (
	errorGroupsSweepInterval = time.Minute
	errorGroupsPerSweep      = 200
	errorGroupsLookback      = 24 * time.Hour
	errorGroupsSweepTimeout  = 50 * time.Second
)

// ErrorGroupsSweep gives a group to every error counted without one: it
// symbolicates one trace of each through the update's index. Whatever the
// traffic, one run symbolicates at most errorGroupsPerSweep traces.
type ErrorGroupsSweep struct {
	explorer *Explorer
	indexes  IndexOpener
}

// NewErrorGroupsSweep binds the error explorer and index opener for a sweep;
// it does not schedule or run a pass.
func NewErrorGroupsSweep(explorer *Explorer, indexes IndexOpener) *ErrorGroupsSweep {
	return &ErrorGroupsSweep{explorer: explorer, indexes: indexes}
}

// Run is one pass. An error whose update has no usable index is left for a
// later pass, when the index may exist. Pending-list, unexpected index, and
// batch-write errors propagate; individual exception-read failures are skipped.
func (s *ErrorGroupsSweep) Run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, errorGroupsSweepTimeout)
	defer cancel()
	pending, err := s.explorer.pendingErrorGroups(ctx, time.Now().Add(-errorGroupsLookback), errorGroupsPerSweep)
	if err != nil {
		return err
	}
	// One index per update for the whole pass; nil when the update has none.
	indexes := map[string]*symbolication.Index{}
	var groups []groupedError
	for _, key := range pending {
		index, err := s.indexOf(ctx, indexes, key)
		if err != nil {
			return err
		}
		if index == nil {
			continue
		}
		group, err := s.symbolicate(ctx, index, key)
		if err != nil {
			log.Printf("observe: error %s of update %s stays without a group: %v", key.fingerprint, key.updateID, err)
			continue
		}
		groups = append(groups, groupedError{errorKey: key, ErrorGroup: group})
	}
	return s.explorer.writeErrorGroups(ctx, groups)
}

// indexOf opens the update's index once per pass. An update without a usable
// index reads as nil; an error reaching the store or the database stops the
// pass, since it would repeat for every error.
func (s *ErrorGroupsSweep) indexOf(ctx context.Context, indexes map[string]*symbolication.Index, key errorKey) (*symbolication.Index, error) {
	cacheKey := key.appID + "/" + key.updateID
	if index, seen := indexes[cacheKey]; seen {
		return index, nil
	}
	index, err := s.indexes.OpenUpdateIndex(ctx, key.appID, key.updateID)
	switch {
	case errors.Is(err, symbolication.ErrNoSourcemap),
		errors.Is(err, symbolication.ErrIndexNotReady),
		errors.Is(err, symbolication.ErrIndexFailed),
		errors.Is(err, symbolication.ErrUpdateNotFound),
		errors.Is(err, symbolication.ErrUnavailable):
		index = nil
	case err != nil:
		return nil, err
	}
	indexes[cacheKey] = index
	return index, nil
}

// symbolicate builds a group from the latest stored exception, returning
// exception-read errors. Frame lookup failures leave unmapped frames.
func (s *ErrorGroupsSweep) symbolicate(ctx context.Context, index *symbolication.Index, key errorKey) (ErrorGroup, error) {
	found, err := s.explorer.oneTraceOf(ctx, key)
	if err != nil {
		return ErrorGroup{}, err
	}
	trace := symbolication.Symbolicate(index, found.stacktrace)
	return ErrorGroup{
		Fingerprint:      key.fingerprint,
		GroupFingerprint: symbolication.GroupFingerprint(found.errorType, found.message, trace).String(),
		ErrorType:        found.errorType,
		Message:          found.message,
		Culprit:          symbolication.Culprit(trace),
		Trace:            trace,
		SymbolicatedAt:   time.Now().UTC(),
	}, nil
}

type errorGroupsSweepArgs struct{}

// Kind identifies error-group sweeps to River.
func (errorGroupsSweepArgs) Kind() string { return "error-groups-sweep" }

// A run still going when the next is due is not doubled.
func (errorGroupsSweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 1,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateScheduled,
				rivertype.JobStateRunning,
			},
		},
	}
}

type errorGroupsWorker struct {
	river.WorkerDefaults[errorGroupsSweepArgs]
	sweep *ErrorGroupsSweep
}

// Work runs one error-group sweep and returns its error to River.
func (w *errorGroupsWorker) Work(ctx context.Context, _ *river.Job[errorGroupsSweepArgs]) error {
	return w.sweep.Run(ctx)
}

// RegisterErrorGroupsWorker adds the sweep worker before the job client starts.
func RegisterErrorGroupsWorker(workers *river.Workers, sweep *ErrorGroupsSweep) {
	river.AddWorker(workers, &errorGroupsWorker{sweep: sweep})
}

// PeriodicJob schedules the sweep; River runs it on one replica only.
func (s *ErrorGroupsSweep) PeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(errorGroupsSweepInterval),
		func() (river.JobArgs, *river.InsertOpts) { return errorGroupsSweepArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}
