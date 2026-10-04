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
	"xprem/internal/database/postgres"

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
	errorGroupsRetryDelay    = 5 * time.Minute
	errorGroupsFinishTimeout = 5 * time.Second
)

// ErrorGroupsSweep gives a group to every error counted without one, from one
// trace symbolicated through the update's index, and marks the errors of an
// update that has no source map.
type ErrorGroupsSweep struct {
	explorer *Explorer
	indexes  IndexOpener
	now      func() time.Time
}

func NewErrorGroupsSweep(explorer *Explorer, indexes IndexOpener) *ErrorGroupsSweep {
	return &ErrorGroupsSweep{explorer: explorer, indexes: indexes, now: time.Now}
}

// Run is one bounded pass. The cursor and retry deadlines survive a worker or
// replica change; an unavailable update cannot hold all other applications up.
func (s *ErrorGroupsSweep) Run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, errorGroupsSweepTimeout)
	defer cancel()
	release, locked, err := postgres.TryAdvisoryLock(ctx, s.explorer.postgres.DB, postgres.ErrorGroupSweepLockID, "error group sweep")
	if err != nil || !locked {
		return err
	}
	defer release()
	state, err := s.explorer.errorGroupsState(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	state.prune(now)
	// Include the oldest partial ingestion hour.
	since := now.Add(-errorGroupsLookback).Truncate(time.Hour)
	appID, err := s.explorer.nextErrorGroupsApp(ctx, since, state)
	if err != nil {
		return err
	}
	if appID == "" {
		return s.explorer.saveErrorGroupsState(ctx, state)
	}
	state.LastApp = appID
	// Rotate even if this app's candidate query times out. This records no
	// candidate progress; the cursor advances only after acknowledged writes.
	if err := s.explorer.saveErrorGroupsState(ctx, state); err != nil {
		return err
	}
	pending, err := s.explorer.pendingErrorGroups(ctx, since, errorGroupsPerSweep, state)
	if err != nil {
		return err
	}
	known := map[string]error{}
	var groups []groupedError
	dirty := true
	// Checkpoint only after the corresponding groups were acknowledged. If a
	// write fails, the next pass repeats that batch rather than losing it.
	flush := func() error {
		if !dirty {
			return nil
		}
		writeCtx := ctx
		if ctx.Err() != nil {
			// Finish only already-attempted work when the run's budget expires.
			// This bounded allowance saves progress past a slow failed index.
			var finishCancel context.CancelFunc
			writeCtx, finishCancel = context.WithTimeout(context.WithoutCancel(ctx), errorGroupsFinishTimeout)
			defer finishCancel()
		}
		if err := s.explorer.writeErrorGroups(writeCtx, groups); err != nil {
			return err
		}
		if err := s.explorer.saveErrorGroupsState(writeCtx, state); err != nil {
			return err
		}
		groups = nil
		dirty = false
		return nil
	}
	for attempted, key := range pending {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, flush())
		}
		index, err := s.indexOf(ctx, known, key)
		var group ErrorGroup
		switch {
		case err == nil:
			group, err = s.symbolicate(ctx, index, key)
			if err != nil {
				log.Printf("observe: error %s of update %s stays without a group: %v", key.fingerprint, key.updateID, err)
			}
		case errors.Is(err, symbolication.ErrNoSourcemap),
			errors.Is(err, symbolication.ErrUpdateNotFound):
			group = ErrorGroup{GroupFingerprint: noGroupFingerprint, SymbolicatedAt: s.now().UTC()}
			err = nil
		case errors.Is(err, symbolication.ErrUnavailable):
			// Nothing can be grouped without a license. Keep this key pending.
			return flush()
		default:
			state.deferUpdate(key, s.now().Add(errorGroupsRetryDelay))
		}
		if err == nil {
			groups = append(groups, groupedError{errorKey: key, ErrorGroup: group})
		}
		state.advance(key, s.now())
		dirty = true
		if (attempted+1)%errorGroupsWriteEvery == 0 {
			if err := flush(); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
	if len(pending) < errorGroupsPerSweep && state.cursor() != nil {
		// End of a traversal: the next pass can see new keys behind the cursor.
		delete(state.Cursors, appID)
		dirty = true
	}
	return errors.Join(ctx.Err(), flush())
}

// indexOf opens the update's index; an update that failed to open once in the
// pass is not asked again. An unreadable trace does not hide its valid siblings.
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
