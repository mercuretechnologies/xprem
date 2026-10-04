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
// back it looks for them, how long it may take, and how many groups it writes
// at a time.
const (
	errorGroupsSweepInterval = time.Minute
	errorGroupsPerSweep      = 200
	errorGroupsLookback      = ErrorsMaxWindow
	errorGroupsSweepTimeout  = 50 * time.Second
	errorGroupsWriteEvery    = 20
)

// ErrorGroupsSweep gives a group to every error counted without one, from one
// trace symbolicated through the update's index, and marks the errors of an
// update that has no source map.
type ErrorGroupsSweep struct {
	explorer *Explorer
	indexes  IndexOpener
}

func NewErrorGroupsSweep(explorer *Explorer, indexes IndexOpener) *ErrorGroupsSweep {
	return &ErrorGroupsSweep{explorer: explorer, indexes: indexes}
}

// Run is one pass.
func (s *ErrorGroupsSweep) Run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, errorGroupsSweepTimeout)
	defer cancel()
	// Include the oldest partial ingestion hour.
	since := time.Now().Add(-errorGroupsLookback).Truncate(time.Hour)
	known := map[string]error{}
	symbolicated, skipped := 0, 0
	var head errorKey
	for symbolicated < errorGroupsPerSweep {
		// The errors written left the list and the skipped ones lead it.
		pending, err := s.explorer.pendingErrorGroups(ctx, since, errorGroupsPerSweep, skipped)
		if err != nil {
			return err
		}
		if len(pending) == 0 || pending[0] == head {
			return nil
		}
		head = pending[0]
		var groups []groupedError
		for _, key := range pending {
			if symbolicated == errorGroupsPerSweep {
				break
			}
			index, err := s.indexOf(ctx, known, key)
			var group ErrorGroup
			switch {
			case err == nil:
				group, err = s.symbolicate(ctx, index, key)
				if err != nil {
					log.Printf("observe: error %s of update %s stays without a group: %v", key.fingerprint, key.updateID, err)
					skipped++
					continue
				}
				symbolicated++
			case errors.Is(err, symbolication.ErrNoSourcemap),
				errors.Is(err, symbolication.ErrUpdateNotFound):
				group = ErrorGroup{GroupFingerprint: noGroupFingerprint, SymbolicatedAt: time.Now().UTC()}
			case errors.Is(err, symbolication.ErrUnavailable):
				// Nothing can be grouped without a license.
				return s.explorer.writeErrorGroups(ctx, groups)
			default:
				skipped++
				continue
			}
			groups = append(groups, groupedError{errorKey: key, ErrorGroup: group})
			if len(groups) == errorGroupsWriteEvery {
				if err := s.explorer.writeErrorGroups(ctx, groups); err != nil {
					return err
				}
				groups = nil
			}
		}
		if err := s.explorer.writeErrorGroups(ctx, groups); err != nil {
			return err
		}
		if len(pending) < errorGroupsPerSweep {
			return nil
		}
	}
	return nil
}

// indexOf opens the update's index; an update that answered with an error
// once in the pass is not asked again.
func (s *ErrorGroupsSweep) indexOf(ctx context.Context, known map[string]error, key errorKey) (*symbolication.Index, error) {
	cacheKey := key.appID + "/" + key.updateID
	if err, seen := known[cacheKey]; seen {
		return nil, err
	}
	index, err := s.indexes.OpenUpdateIndex(ctx, key.appID, key.updateID)
	if err != nil {
		if _, state := errorGroupStatusOf(err); !state {
			log.Printf("observe: the index of update %s cannot be opened: %v", key.updateID, err)
		}
		known[cacheKey] = err
	}
	return index, err
}

func (s *ErrorGroupsSweep) symbolicate(ctx context.Context, index *symbolication.Index, key errorKey) (ErrorGroup, error) {
	found, err := s.explorer.oneTraceOf(ctx, key)
	if err != nil {
		return ErrorGroup{}, err
	}
	trace := symbolication.Symbolicate(index, found.stacktrace)
	return ErrorGroup{
		GroupFingerprint: symbolication.GroupFingerprint(found.errorType, found.message, trace).String(),
		ErrorType:        found.errorType,
		Message:          found.message,
		Culprit:          symbolication.Culprit(trace),
		Trace:            trace,
		SymbolicatedAt:   time.Now().UTC(),
	}, nil
}

type errorGroupsSweepArgs struct{}

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

func (w *errorGroupsWorker) Work(ctx context.Context, _ *river.Job[errorGroupsSweepArgs]) error {
	return w.sweep.Run(ctx)
}

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
