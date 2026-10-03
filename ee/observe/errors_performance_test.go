// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"xprem/internal/database/clickhouse"
)

// Opt in with OBSERVE_ERRORS_PERF_ROWS=1000000 and the usual live-store URLs.
// Use a disposable database: this fixture deliberately leaves its isolated app
// behind so EXPLAIN and system.query_log can be inspected after the test.
func TestErrorsPerformance31Days(t *testing.T) {
	raw := os.Getenv("OBSERVE_ERRORS_PERF_ROWS")
	if raw == "" {
		t.Skip("set OBSERVE_ERRORS_PERF_ROWS to run the 31-day load fixture")
	}
	count, err := strconv.ParseUint(raw, 10, 64)
	require.NoError(t, err)
	require.GreaterOrEqual(t, count, uint64(10000))
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)
	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()
	appID := uuid.NewString()
	from := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	to := from.Add(ErrorsMaxWindow)
	// One in ten logs is an error. A group occurs across sixteen updates;
	// devices recur across updates, with a mix of models, OS and runtimes.
	require.NoError(t, engine.Conn.Exec(ctx, `INSERT INTO observe_logs
		(app_id, update_id, eas_client_id, session_id, timestamp, content_key,
		 event_name, severity_number, is_fatal, body, attributes,
		 error_fingerprint, platform, runtime_version, device_model, os_name, os_version)
		SELECT toUUID(?), reinterpretAsUUID(MD5(concat('update', toString(intDiv(number,1000)%16)))),
		 reinterpretAsUUID(MD5(concat('device',toString(number%20000)))),
		 reinterpretAsUUID(MD5(concat('session',toString(intDiv(number,50))))),
		 toDateTime64(?,9) + toIntervalSecond(intDiv(number*2678400,?)),
		 reinterpretAsUUID(MD5(concat(?,toString(number)))),
		 if(number%10=0,'exception','navigation'),if(number%10=0,17,9),
		 toUInt8(number%110=0),'Synthetic production-shaped telemetry event',
		 if(number%10=0,concat('{"exception.type":"TypeError","exception.message":"Failure ',
		 toString(intDiv(number,10)%100),'","exception.stacktrace":"at screen (app.js:100:20)"}'),'{}'),
		 if(number%10=0,reinterpretAsUUID(MD5(concat('error',toString(intDiv(number,10)%100)))),toUUID(?)),
		 if(intDiv(number,1000)%2=0,'ios','android'),concat('runtime-',toString(intDiv(number,100)%4)),
		 concat('model-',toString(intDiv(number,10)%12)),if(intDiv(number,1000)%2=0,'iOS','Android'),
		 concat('18.',toString(intDiv(number,10)%3)) FROM numbers(?)`,
		appID, from, count, appID, ZeroUpdateID, count))
	// Retries retain their content key and event time, but arrive later.
	require.NoError(t, engine.Conn.Exec(ctx, `INSERT INTO observe_logs SELECT * REPLACE (ingested_at + INTERVAL 1 SECOND AS ingested_at)
		FROM observe_logs WHERE app_id = ? AND sipHash64(content_key)%10=0`, appID))
	require.NoError(t, engine.Conn.Exec(ctx, `INSERT INTO error_groups
		(app_id,update_id,error_fingerprint,group_fingerprint,error_type,message,culprit,trace,symbolicated_at)
		SELECT app_id,update_id,error_fingerprint,error_fingerprint,'TypeError',
		 any(JSONExtractString(attributes,'exception.message')),'Screen.tsx in onPress','{}',now()
		FROM observe_logs WHERE app_id=? AND error_fingerprint!=toUUID(?)
		GROUP BY app_id,update_id,error_fingerprint`, appID, ZeroUpdateID))
	var stored uint64
	require.NoError(t, engine.Conn.QueryRow(ctx, "SELECT count() FROM observe_logs WHERE app_id=?", appID).Scan(&stored))
	t.Logf("fixture app=%s distinct_logs=%d stored_logs=%d expected_errors=%d period=%s..%s", appID, count, stored, (count+9)/10, from.Format(time.RFC3339), to.Format(time.RFC3339))
	tracked := &errorsMeasuredConn{Conn: engine.Conn}
	explorer := &Explorer{clickhouse: &clickhouse.Engine{Conn: tracked}}
	base := ExplorerQuery{From: from, To: to, Bucket: 24 * time.Hour}
	var first ErrorSummary
	for _, series := range []bool{false, true} {
		for _, limit := range []int{1, 100} {
			tracked.calls = nil
			started := time.Now()
			page, err := explorer.ReadErrors(ctx, appID, ErrorsQuery{ExplorerQuery: base, IncludeSeries: series, Limit: limit})
			require.NoError(t, err)
			t.Logf("list series=%v limit=%d wall_ms=%d queries=%d", series, limit, time.Since(started).Milliseconds(), len(tracked.calls))
			require.Len(t, page.Errors, limit)
			first = page.Errors[0]
			budget := 1
			if series {
				budget = 2
			}
			require.Len(t, tracked.calls, budget, "query count must not grow with page size")
			var total uint64
			for _, row := range page.Errors {
				total += row.Occurrences
				if series {
					var sum uint64
					for _, point := range row.Series {
						sum += point.Count
					}
					require.Equal(t, row.Occurrences, sum)
				}
			}
			if limit == 100 {
				require.Equal(t, (count+9)/10, total)
			}
			started = time.Now()
			cached, err := explorer.ReadErrors(ctx, appID, ErrorsQuery{ExplorerQuery: base, IncludeSeries: series, Limit: limit})
			require.NoError(t, err)
			require.Equal(t, page, cached)
			require.Len(t, tracked.calls, budget, "an identical cached read must add no queries")
			t.Logf("list cache_hit series=%v limit=%d wall_us=%d queries=0", series, limit, time.Since(started).Microseconds())
			for _, call := range tracked.calls {
				reportErrorsQuery(t, engine.Conn, call)
			}
		}
	}
	var representative ObserveLog
	for _, limit := range []int{1, 100} {
		tracked.calls = nil
		started := time.Now()
		details, err := explorer.ReadErrorDetails(ctx, appID, first.ErrorID, ErrorDetailsQuery{ExplorerQuery: base, Limit: limit})
		require.NoError(t, err)
		t.Logf("detail limit=%d wall_ms=%d queries=%d", limit, time.Since(started).Milliseconds(), len(tracked.calls))
		require.Len(t, tracked.calls, 2, "summary, histogram and four breakdowns share one query; occurrences use one query")
		require.NotNil(t, details.Summary)
		require.Equal(t, first.Occurrences, details.Summary.Occurrences)
		require.Len(t, details.Occurrences, min(limit, int(first.Occurrences)))
		representative = details.Occurrences[0]
		var sum uint64
		for _, point := range details.Series {
			sum += point.Count
		}
		require.Equal(t, first.Occurrences, sum)
		for _, segments := range [][]ErrorBreakdown{details.Updates, details.DeviceModels, details.OSVersions, details.Runtimes} {
			sum = 0
			var percentage float64
			for _, segment := range segments {
				sum += segment.Occurrences
				percentage += segment.Percentage
			}
			require.Equal(t, first.Occurrences, sum)
			require.InDelta(t, 100, percentage, 0.000001)
		}
		started = time.Now()
		cached, err := explorer.ReadErrorDetails(ctx, appID, first.ErrorID, ErrorDetailsQuery{ExplorerQuery: base, Limit: limit})
		require.NoError(t, err)
		require.Equal(t, details, cached)
		require.Len(t, tracked.calls, 2, "an identical cached read must add no queries")
		t.Logf("detail cache_hit limit=%d wall_us=%d queries=0", limit, time.Since(started).Microseconds())
		for _, call := range tracked.calls {
			reportErrorsQuery(t, engine.Conn, call)
		}
	}
	fallback, err := json.Marshal([]string{representative.UpdateID, representative.ErrorFingerprint, "", "", "", "", "", "v1"})
	require.NoError(t, err)
	tracked.calls = nil
	started := time.Now()
	details, err := explorer.ReadErrorDetails(ctx, appID, encodeErrorID("f:"+string(fallback)), ErrorDetailsQuery{ExplorerQuery: base, Limit: 100})
	require.NoError(t, err)
	require.NotNil(t, details.Summary)
	require.Equal(t, first.ErrorID, details.Summary.ErrorID)
	require.Len(t, tracked.calls, 3, "an old fallback reference adds exactly one targeted resolution query")
	t.Logf("detail fallback limit=100 wall_ms=%d queries=%d", time.Since(started).Milliseconds(), len(tracked.calls))
	for _, call := range tracked.calls {
		reportErrorsQuery(t, engine.Conn, call)
	}
}

type errorsMeasuredQuery struct {
	id, sql string
	args    []any
}
type errorsMeasuredConn struct {
	driver.Conn
	calls []errorsMeasuredQuery
}

func (c *errorsMeasuredConn) Query(ctx context.Context, sql string, args ...any) (driver.Rows, error) {
	id := "errors-perf-" + uuid.NewString()
	c.calls = append(c.calls, errorsMeasuredQuery{id: id, sql: sql, args: args})
	return c.Conn.Query(chdriver.Context(ctx, chdriver.WithQueryID(id)), sql, args...)
}

func (c *errorsMeasuredConn) QueryRow(ctx context.Context, sql string, args ...any) driver.Row {
	id := "errors-perf-" + uuid.NewString()
	c.calls = append(c.calls, errorsMeasuredQuery{id: id, sql: sql, args: args})
	return c.Conn.QueryRow(chdriver.Context(ctx, chdriver.WithQueryID(id)), sql, args...)
}

func reportErrorsQuery(t *testing.T, conn driver.Conn, call errorsMeasuredQuery) {
	t.Helper()
	ctx := context.Background()
	rows, err := conn.Query(ctx, "EXPLAIN indexes=1 "+call.sql, call.args...)
	require.NoError(t, err)
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		t.Log(line)
	}
	require.NoError(t, rows.Err())
	rows.Close()
	if err := conn.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Logf("query_log unavailable: %v", err)
		return
	}
	var elapsed, readRows, readBytes, memory uint64
	err = conn.QueryRow(ctx, `SELECT query_duration_ms,read_rows,read_bytes,memory_usage
		FROM system.query_log WHERE query_id=? AND type='QueryFinish' ORDER BY event_time_microseconds DESC LIMIT 1`, call.id).Scan(&elapsed, &readRows, &readBytes, &memory)
	if err != nil {
		t.Logf("query_log unavailable for %s: %v", call.id, err)
		return
	}
	t.Log(fmt.Sprintf("query=%s duration_ms=%d read_rows=%d read_bytes=%d peak_memory_bytes=%d", call.id, elapsed, readRows, readBytes, memory))
}
