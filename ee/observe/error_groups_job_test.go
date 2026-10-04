// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"xprem/ee/symbolication"
	"xprem/internal/database"
	"xprem/internal/database/clickhouse"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func errorGroupsTestPostgres(t *testing.T, pgURL string) *database.Engine {
	t.Helper()
	postgres.RunDBMigrations(pgURL)
	pool, err := pgxpool.New(context.Background(), pgURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	engine := &database.Engine{Queries: pgdb.New(pool), DB: pool}
	original, err := engine.GetObserveErrorGroupSweepState(context.Background())
	require.NoError(t, err)
	require.NoError(t, engine.SaveObserveErrorGroupSweepState(context.Background(), []byte("{}")))
	t.Cleanup(func() { require.NoError(t, engine.SaveObserveErrorGroupSweepState(context.Background(), original)) })
	return engine
}

// The adversarial fixtures must not affect other store tests or depend on the
// global pending errors left by them. Both the schema and data are disposable.
func isolatedErrorGroupsExplorer(t *testing.T) *Explorer {
	t.Helper()
	chURL, pgURL := requireLiveStores(t)
	pg := errorGroupsTestPostgres(t, pgURL)
	admin, err := clickhouse.NewClickHouseEngine(context.Background(), chURL)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	name := "error_sweep_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Conn.Exec(context.Background(), "CREATE DATABASE "+name))
	t.Cleanup(func() { require.NoError(t, admin.Conn.Exec(context.Background(), "DROP DATABASE "+name)) })
	dsn, err := url.Parse(chURL)
	require.NoError(t, err)
	dsn.Path = "/" + name
	clickhouse.RunDBMigrations(dsn.String(), pgURL)
	ch, err := clickhouse.NewClickHouseEngine(context.Background(), dsn.String())
	require.NoError(t, err)
	t.Cleanup(ch.Close)
	return &Explorer{postgres: pg, clickhouse: ch}
}

func TestErrorGroupsSweepFairAcrossWorkersAndRetryDelay(t *testing.T) {
	explorer := isolatedErrorGroupsExplorer(t)
	ctx := context.Background()
	brokenApp, readyApp := "00000000-0000-0000-0000-000000000001", "ffffffff-ffff-ffff-ffff-fffffffffffe"
	brokenUpdate, readyUpdate := uuid.NewString(), uuid.NewString()
	require.NoError(t, explorer.clickhouse.Conn.Exec(ctx, `INSERT INTO error_occurrences
		(app_id,update_id,error_fingerprint,hour,title,occurrences,crashes,devices,first_seen,last_seen)
		SELECT toUUID(?),toUUID(?),reinterpretAsUUID(MD5(toString(number))),toStartOfHour(now()),
		'failed indexed update',toUInt64(2),toUInt64(0),uniqState(toUUID(?)),now64(9),now64(9)
		FROM numbers(200000) GROUP BY number`, brokenApp, brokenUpdate, uuid.NewString()))
	ready := errorLogRow(readyApp, readyUpdate, uuid.NewString(), errorAttributes("Error", "ready",
		"Error: ready\n    at onPress (address at /data/app.bundle:1:120)"), 21, true, time.Now().UTC())
	require.NoError(t, NewClickHouseTelemetrySink(explorer.clickhouse).InsertLogs(ctx, []LogRow{ready}))
	tracked := &errorsMeasuredConn{Conn: explorer.clickhouse.Conn}
	explorer.clickhouse = &clickhouse.Engine{Conn: tracked}
	now := time.Now()
	brokenOpens, readyOpens := 0, 0
	repaired := false
	opener := indexOpenerFunc(func(_ context.Context, app, _ string) (*symbolication.Index, error) {
		if app == brokenApp {
			brokenOpens++
			if repaired {
				return nil, symbolication.ErrNoSourcemap
			}
			return nil, symbolication.ErrIndexFailed
		}
		readyOpens++
		return labIndex(t), nil
	})
	newWorker := func() *ErrorGroupsSweep {
		sweep := NewErrorGroupsSweep(explorer, opener)
		sweep.now = func() time.Time { return now }
		return sweep
	}
	started := time.Now()
	require.NoError(t, newWorker().Run(ctx))
	require.Len(t, tracked.calls, 2, "200,000 failed keys require one app query and one bounded candidate query")
	require.Equal(t, 1, brokenOpens, "the same failed update is opened once")
	require.Zero(t, readyOpens)
	state, err := explorer.errorGroupsState(ctx)
	require.NoError(t, err)
	require.NotNil(t, state.cursor())
	require.Len(t, state.Deferred, 1)
	tracked.calls = nil
	require.NoError(t, newWorker().Run(ctx))
	require.Len(t, tracked.calls, 3, "one app query, one candidate query and the ready error's trace")
	require.Equal(t, 1, readyOpens, "another worker reaches a less frequent error of another app on pass two")
	require.Equal(t, 1, brokenOpens)
	group, err := explorer.ReadErrorGroup(ctx, readyApp, readyUpdate, ready.ErrorFingerprint.String())
	require.NoError(t, err)
	require.NotNil(t, group)
	state, err = explorer.errorGroupsState(ctx)
	require.NoError(t, err)
	require.Nil(t, state.cursor(), "the end of the traversal clears the cursor")
	require.NoError(t, newWorker().Run(ctx))
	require.Equal(t, 1, brokenOpens, "a cursor wrap must retain the retry deadline")
	repaired = true
	now = now.Add(errorGroupsRetryDelay)
	tracked.calls = nil
	require.NoError(t, newWorker().Run(ctx))
	require.Equal(t, 2, brokenOpens, "expired retries are attempted again")
	require.Len(t, tracked.calls, 2)
	state, err = explorer.errorGroupsState(ctx)
	require.NoError(t, err)
	require.Empty(t, state.Deferred, "expired deadlines are removed from persistent state")
	var marked uint64
	require.NoError(t, explorer.clickhouse.Conn.QueryRow(ctx, "SELECT count() FROM error_groups WHERE app_id=?", brokenApp).Scan(&marked))
	require.EqualValues(t, errorGroupsPerSweep, marked, "no-source-map marks consume the same bounded budget")
	t.Logf("200000 failed fingerprints: other app grouped on pass 2 in %s", time.Since(started))
}

func TestErrorGroupsSweepBoundsAllCandidatesAndWraps(t *testing.T) {
	explorer := isolatedErrorGroupsExplorer(t)
	ctx := context.Background()
	app, update := "00000000-0000-0000-0000-000000000001", uuid.NewString()
	otherApp, otherUpdate := "ffffffff-ffff-ffff-ffff-fffffffffffe", uuid.NewString()
	var rows []LogRow
	for offset := 0; offset < errorGroupsPerSweep+50; offset++ {
		rows = append(rows, errorLogRow(app, update, uuid.NewString(), errorAttributes("Error", "boom",
			fmt.Sprintf("Error: boom\n    at onPress (address at /data/app.bundle:1:%d)", 1000+offset)), 21, true, time.Now().UTC()))
	}
	require.NoError(t, NewClickHouseTelemetrySink(explorer.clickhouse).InsertLogs(ctx, rows))
	other := errorLogRow(otherApp, otherUpdate, uuid.NewString(), errorAttributes("Error", "sparse app",
		"Error: sparse app\n    at onPress (address at /data/app.bundle:1:120)"), 21, true, time.Now().UTC())
	require.NoError(t, NewClickHouseTelemetrySink(explorer.clickhouse).InsertLogs(ctx, []LogRow{other}))
	tracked := &errorsMeasuredConn{Conn: explorer.clickhouse.Conn}
	explorer.clickhouse = &clickhouse.Engine{Conn: tracked}
	opens := 0
	opener := indexOpenerFunc(func(context.Context, string, string) (*symbolication.Index, error) {
		opens++
		return labIndex(t), nil
	})
	require.NoError(t, NewErrorGroupsSweep(explorer, opener).Run(ctx))
	require.Equal(t, errorGroupsPerSweep, opens)
	require.Len(t, tracked.calls, errorGroupsPerSweep+2, "one app query and one candidate query plus at most 200 trace reads")
	state, err := explorer.errorGroupsState(ctx)
	require.NoError(t, err)
	require.NotNil(t, state.cursor())
	// Change counts behind the cursor. Keyset traversal is unaffected.
	require.NoError(t, NewClickHouseTelemetrySink(explorer.clickhouse).InsertLogs(ctx, rows[:1]))
	require.NoError(t, NewErrorGroupsSweep(explorer, opener).Run(ctx))
	require.Equal(t, errorGroupsPerSweep+1, opens, "a large ready app yields to another app on pass two")
	group, err := explorer.ReadErrorGroup(ctx, otherApp, otherUpdate, other.ErrorFingerprint.String())
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NoError(t, NewErrorGroupsSweep(explorer, opener).Run(ctx))
	require.Equal(t, len(rows)+1, opens)
	state, err = explorer.errorGroupsState(ctx)
	require.NoError(t, err)
	require.Nil(t, state.cursor())
	// A new fingerprint after traversal, even if it sorts before the old
	// cursor, must be seen on the next pass.
	newRow := errorLogRow(app, update, uuid.NewString(), errorAttributes("Error", "new",
		"Error: new\n    at newSite (address at /data/app.bundle:1:5000)"), 21, true, time.Now().UTC())
	require.NoError(t, NewClickHouseTelemetrySink(explorer.clickhouse).InsertLogs(ctx, []LogRow{newRow}))
	require.NoError(t, NewErrorGroupsSweep(explorer, opener).Run(ctx))
	require.Equal(t, len(rows)+2, opens)
}

func TestErrorGroupsSweepRotatesPastLargeUnmappedApp(t *testing.T) {
	for _, unavailable := range []error{symbolication.ErrNoSourcemap, symbolication.ErrUpdateNotFound} {
		t.Run(unavailable.Error(), func(t *testing.T) {
			explorer := isolatedErrorGroupsExplorer(t)
			ctx := context.Background()
			largeApp, readyApp := "00000000-0000-0000-0000-000000000001", "ffffffff-ffff-ffff-ffff-fffffffffffe"
			largeUpdate, readyUpdate := uuid.NewString(), uuid.NewString()
			require.NoError(t, explorer.clickhouse.Conn.Exec(ctx, `INSERT INTO error_occurrences
				(app_id,update_id,error_fingerprint,hour,title,occurrences,crashes,devices,first_seen,last_seen)
				SELECT toUUID(?),toUUID(?),reinterpretAsUUID(MD5(toString(number))),toStartOfHour(now()),
				'no source map',toUInt64(2),toUInt64(0),uniqState(toUUID(?)),now64(9),now64(9)
				FROM numbers(200000) GROUP BY number`, largeApp, largeUpdate, uuid.NewString()))
			ready := errorLogRow(readyApp, readyUpdate, uuid.NewString(), errorAttributes("Error", "ready",
				"Error: ready\n    at onPress (address at /data/app.bundle:1:120)"), 21, true, time.Now().UTC())
			require.NoError(t, NewClickHouseTelemetrySink(explorer.clickhouse).InsertLogs(ctx, []LogRow{ready}))
			tracked := &errorsMeasuredConn{Conn: explorer.clickhouse.Conn}
			explorer.clickhouse = &clickhouse.Engine{Conn: tracked}
			opener := indexOpenerFunc(func(_ context.Context, app, _ string) (*symbolication.Index, error) {
				if app == largeApp {
					return nil, unavailable
				}
				return labIndex(t), nil
			})
			require.NoError(t, NewErrorGroupsSweep(explorer, opener).Run(ctx))
			require.Len(t, tracked.calls, 2)
			state, err := explorer.errorGroupsState(ctx)
			require.NoError(t, err)
			require.Equal(t, largeApp, state.LastApp)
			require.Empty(t, state.Deferred, "missing source maps consume bounded marks without artificial cooldown")
			tracked.calls = nil
			require.NoError(t, NewErrorGroupsSweep(explorer, opener).Run(ctx))
			require.Len(t, tracked.calls, 3)
			group, err := explorer.ReadErrorGroup(ctx, readyApp, readyUpdate, ready.ErrorFingerprint.String())
			require.NoError(t, err)
			require.NotNil(t, group, "200,000 unmapped keys of another app must not delay a ready app beyond pass two")
			require.NoError(t, NewErrorGroupsSweep(explorer, opener).Run(ctx))
			var marked uint64
			require.NoError(t, explorer.clickhouse.Conn.QueryRow(ctx, "SELECT count() FROM error_groups WHERE app_id=?", largeApp).Scan(&marked))
			require.EqualValues(t, 2*errorGroupsPerSweep, marked, "the unmapped app resumes at full batch size after rotation wraps")
		})
	}
}

type errorGroupsFailQueryConn struct {
	driver.Conn
	fail bool
}

func (c *errorGroupsFailQueryConn) Query(ctx context.Context, query string, args ...any) (driver.Rows, error) {
	if c.fail && strings.Contains(query, "SELECT toString(app_id), toString(update_id)") {
		return nil, errors.New("candidate query failed")
	}
	return c.Conn.Query(ctx, query, args...)
}

func TestErrorGroupsSweepRotatesAfterCandidateQueryFails(t *testing.T) {
	explorer := isolatedErrorGroupsExplorer(t)
	ctx := context.Background()
	largeApp, readyApp := "00000000-0000-0000-0000-000000000001", "ffffffff-ffff-ffff-ffff-fffffffffffe"
	var ready LogRow
	for _, app := range []string{largeApp, readyApp} {
		row := errorLogRow(app, uuid.NewString(), uuid.NewString(), errorAttributes("Error", "ready",
			"Error: ready\n    at onPress (address at /data/app.bundle:1:120)"), 21, true, time.Now().UTC())
		require.NoError(t, NewClickHouseTelemetrySink(explorer.clickhouse).InsertLogs(ctx, []LogRow{row}))
		ready = row
	}
	conn := &errorGroupsFailQueryConn{Conn: explorer.clickhouse.Conn, fail: true}
	explorer.clickhouse = &clickhouse.Engine{Conn: conn}
	opener := indexOpenerFunc(func(context.Context, string, string) (*symbolication.Index, error) { return labIndex(t), nil })
	require.ErrorContains(t, NewErrorGroupsSweep(explorer, opener).Run(ctx), "candidate query failed")
	state, err := explorer.errorGroupsState(ctx)
	require.NoError(t, err)
	require.Equal(t, largeApp, state.LastApp, "a failing tenant query must yield to other tenants")
	require.Nil(t, state.cursor(), "no candidate was processed")
	conn.fail = false
	require.NoError(t, NewErrorGroupsSweep(explorer, opener).Run(ctx))
	group, err := explorer.ReadErrorGroup(ctx, readyApp, ready.UpdateID, ready.ErrorFingerprint.String())
	require.NoError(t, err)
	require.NotNil(t, group)
	// The first app's untouched error is still pending and is recovered on wrap.
	require.NoError(t, NewErrorGroupsSweep(explorer, opener).Run(ctx))
	require.Empty(t, pendingKeys(t, explorer, time.Now().Add(-time.Hour), 10))
}

func TestErrorGroupsSweepPreservesAppCursorPastUnreadableTraces(t *testing.T) {
	explorer := isolatedErrorGroupsExplorer(t)
	ctx := context.Background()
	largeApp, readyApp := "00000000-0000-0000-0000-000000000001", "ffffffff-ffff-ffff-ffff-fffffffffffe"
	update := uuid.NewString()
	// 205 aggregates have no underlying traces. Their numeric fingerprints
	// precede the readable error of the SAME update in native UUID order.
	require.NoError(t, explorer.clickhouse.Conn.Exec(ctx, `INSERT INTO error_occurrences
		(app_id,update_id,error_fingerprint,hour,title,occurrences,crashes,devices,first_seen,last_seen)
		SELECT toUUID(?),toUUID(?),toUUID(concat('00000000-0000-0000-0000-', leftPad(toString(number+1),12,'0'))),
		toStartOfHour(now()),'unreadable trace',toUInt64(2),toUInt64(0),uniqState(toUUID(?)),now64(9),now64(9)
		FROM numbers(?) GROUP BY number`, largeApp, update, uuid.NewString(), errorGroupsPerSweep+5))
	readable := errorLogRow(largeApp, update, uuid.NewString(), errorAttributes("Error", "readable sibling",
		"Error: readable sibling\n    at onPress (address at /data/app.bundle:1:120)"), 21, true, time.Now().UTC())
	readable.ErrorFingerprint = uuid.MustParse("ffffffff-ffff-ffff-ffff-fffffffffffe")
	other := errorLogRow(readyApp, uuid.NewString(), uuid.NewString(), errorAttributes("Error", "sparse app",
		"Error: sparse app\n    at onPress (address at /data/app.bundle:1:120)"), 21, true, time.Now().UTC())
	require.NoError(t, NewClickHouseTelemetrySink(explorer.clickhouse).InsertLogs(ctx, []LogRow{readable, other}))
	tracked := &errorsMeasuredConn{Conn: explorer.clickhouse.Conn}
	explorer.clickhouse = &clickhouse.Engine{Conn: tracked}
	index := labIndex(t)
	opener := indexOpenerFunc(func(context.Context, string, string) (*symbolication.Index, error) { return index, nil })
	require.NoError(t, NewErrorGroupsSweep(explorer, opener).Run(ctx))
	require.Len(t, tracked.calls, errorGroupsPerSweep+2)
	state, err := explorer.errorGroupsState(ctx)
	require.NoError(t, err)
	require.NotNil(t, state.cursor())
	require.Empty(t, state.Deferred, "one bad trace must not defer all errors of its update")
	tracked.calls = nil
	require.NoError(t, NewErrorGroupsSweep(explorer, opener).Run(ctx))
	require.Len(t, tracked.calls, 3)
	group, err := explorer.ReadErrorGroup(ctx, readyApp, other.UpdateID, other.ErrorFingerprint.String())
	require.NoError(t, err)
	require.NotNil(t, group, "the other app is processed on pass two")
	state, err = explorer.errorGroupsState(ctx)
	require.NoError(t, err)
	require.Contains(t, state.Cursors, largeApp, "app rotation must retain the unreadable-prefix cursor")
	tracked.calls = nil
	require.NoError(t, NewErrorGroupsSweep(explorer, opener).Run(ctx))
	require.Len(t, tracked.calls, 8, "two selection queries, five unreadable traces and their readable sibling")
	group, err = explorer.ReadErrorGroup(ctx, largeApp, update, readable.ErrorFingerprint.String())
	require.NoError(t, err)
	require.NotNil(t, group, "a readable sibling beyond 200 bad traces is reached on the app's next turn")
	state, err = explorer.errorGroupsState(ctx)
	require.NoError(t, err)
	require.NotContains(t, state.Cursors, largeApp, "only this app's exhausted cursor is cleared")
}

type errorGroupsFailWriteConn struct {
	driver.Conn
	fail bool
}

func (c *errorGroupsFailWriteConn) PrepareBatch(ctx context.Context, query string, options ...driver.PrepareBatchOption) (driver.Batch, error) {
	if c.fail {
		return nil, errors.New("group write failed")
	}
	return c.Conn.PrepareBatch(ctx, query, options...)
}

func TestErrorGroupsSweepDoesNotCheckpointUnwrittenGroups(t *testing.T) {
	explorer := isolatedErrorGroupsExplorer(t)
	ctx := context.Background()
	app, update := uuid.NewString(), uuid.NewString()
	var rows []LogRow
	for offset := 0; offset < errorGroupsWriteEvery+1; offset++ {
		rows = append(rows, errorLogRow(app, update, uuid.NewString(), errorAttributes("Error", "boom",
			fmt.Sprintf("Error: boom\n    at onPress (address at /data/app.bundle:1:%d)", offset)), 21, true, time.Now().UTC()))
	}
	require.NoError(t, NewClickHouseTelemetrySink(explorer.clickhouse).InsertLogs(ctx, rows))
	conn := &errorGroupsFailWriteConn{Conn: explorer.clickhouse.Conn, fail: true}
	explorer.clickhouse = &clickhouse.Engine{Conn: conn}
	opener := indexOpenerFunc(func(context.Context, string, string) (*symbolication.Index, error) { return labIndex(t), nil })
	require.ErrorContains(t, NewErrorGroupsSweep(explorer, opener).Run(ctx), "group write failed")
	state, err := explorer.errorGroupsState(ctx)
	require.NoError(t, err)
	require.Nil(t, state.cursor(), "a failed write must not move past an unwritten batch")
	conn.fail = false
	require.NoError(t, NewErrorGroupsSweep(explorer, opener).Run(ctx))
	var grouped uint64
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM error_groups").Scan(&grouped))
	require.EqualValues(t, len(rows), grouped, "all unwritten candidates are retried by another worker")
}

func TestErrorGroupsSweepSavesFailedAttemptOnCancellation(t *testing.T) {
	explorer := isolatedErrorGroupsExplorer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app, update := uuid.NewString(), uuid.NewString()
	var rows []LogRow
	for offset := 0; offset < 2; offset++ {
		rows = append(rows, errorLogRow(app, update, uuid.NewString(), errorAttributes("Error", "boom",
			fmt.Sprintf("Error: boom\n    at onPress (address at /data/app.bundle:1:%d)", offset)), 21, true, time.Now().UTC()))
	}
	require.NoError(t, NewClickHouseTelemetrySink(explorer.clickhouse).InsertLogs(ctx, rows))
	opens := 0
	sweep := NewErrorGroupsSweep(explorer, indexOpenerFunc(func(context.Context, string, string) (*symbolication.Index, error) {
		opens++
		cancel()
		return nil, context.Canceled
	}))
	require.ErrorIs(t, sweep.Run(ctx), context.Canceled)
	require.Equal(t, 1, opens)
	state, err := explorer.errorGroupsState(context.Background())
	require.NoError(t, err)
	require.NotNil(t, state.cursor())
	require.Len(t, state.Deferred, 1, "a slow failed attempt must not recur at the front of every run")
	pending, err := explorer.pendingErrorGroups(context.Background(), time.Now().Add(-time.Hour), errorGroupsPerSweep, errorGroupsSweepState{LastApp: app, Cursors: state.Cursors})
	require.NoError(t, err)
	require.Len(t, pending, 1, "the untouched candidate remains ahead of the saved cursor")
}

func TestErrorGroupsSweepRetryStateIsBoundedAndExpires(t *testing.T) {
	var state errorGroupsSweepState
	now := time.Now()
	for i := 0; i < errorGroupsMaxDeferredUpdates+1; i++ {
		state.deferUpdate(errorKey{appID: uuid.NewString(), updateID: uuid.NewString()}, now.Add(time.Duration(i)*time.Second))
	}
	require.Len(t, state.Deferred, errorGroupsMaxDeferredUpdates)
	state.prune(now.Add(time.Duration(errorGroupsMaxDeferredUpdates) * time.Second))
	require.Empty(t, state.Deferred)
	oldest := errorKey{appID: "oldest", updateID: "update", fingerprint: "fingerprint"}
	state.advance(oldest, now)
	for i := 0; i < errorGroupsMaxAppCursors; i++ {
		state.advance(errorKey{appID: fmt.Sprintf("app-%04d", i), updateID: "update", fingerprint: "fingerprint"}, now.Add(time.Duration(i+1)*time.Second))
	}
	require.Len(t, state.Cursors, errorGroupsMaxAppCursors)
	require.NotContains(t, state.Cursors, oldest.appID, "the least recently used cursor is evicted")
	state.prune(now.Add(errorGroupsLookback + time.Duration(errorGroupsMaxAppCursors)*time.Second))
	require.Empty(t, state.Cursors, "cursor expiry follows the telemetry lookback")
}

func TestErrorGroupsSweepDoesNotRunWhileAnotherReplicaHoldsLock(t *testing.T) {
	explorer := isolatedErrorGroupsExplorer(t)
	ctx := context.Background()
	tracked := &errorsMeasuredConn{Conn: explorer.clickhouse.Conn}
	explorer.clickhouse = &clickhouse.Engine{Conn: tracked}
	release, held, err := postgres.TryAdvisoryLock(ctx, explorer.postgres.DB, postgres.ErrorGroupSweepLockID, "test sweep")
	require.NoError(t, err)
	require.True(t, held)
	defer release()
	sweep := NewErrorGroupsSweep(explorer, indexOpenerFunc(func(context.Context, string, string) (*symbolication.Index, error) {
		t.Fatal("a second replica must not open indexes or change progress")
		return nil, nil
	}))
	require.NoError(t, sweep.Run(ctx))
	require.Empty(t, tracked.calls, "the losing replica must not query candidates")
	release()
	require.NoError(t, sweep.Run(ctx))
	require.Len(t, tracked.calls, 1, "the next replica resumes after the lock is released")
}
