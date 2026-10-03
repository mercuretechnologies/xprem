// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"xprem/internal/database/clickhouse"
)

// Run with OBSERVE_UPDATE_ERRORS_PERF_ROWS=1000000 and live-store URLs.
// Use a disposable database: fixtures remain for EXPLAIN and query_log reads.
func TestUpdateErrorsPerformance(t *testing.T) {
	raw := os.Getenv("OBSERVE_UPDATE_ERRORS_PERF_ROWS")
	if raw == "" {
		t.Skip("set OBSERVE_UPDATE_ERRORS_PERF_ROWS to run the retained-history load fixture")
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
	appID, updateID := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Second)
	currentCount := count / 10
	historicalCount := count - currentCount
	currentFrom := now.Add(-ErrorsMaxWindow)
	historicalFrom := now.Add(-365 * 24 * time.Hour)

	// Every log is an error. The current update has 100 canonical groups;
	// historical updates share the first 25 and have 975 unrelated groups.
	// The shared groups appear latest on the current update so the UI's
	// 25-row page exercises a populated history lookup at the million-row size.
	// Spread ingestion as well as event time to exercise daily aggregate parts.
	require.NoError(t, engine.Conn.Exec(ctx, `INSERT INTO observe_logs
		(app_id,update_id,eas_client_id,session_id,timestamp,ingested_at,content_key,
		 event_name,severity_number,is_fatal,body,error_fingerprint,platform,runtime_version)
		SELECT toUUID(?),toUUID(?),reinterpretAsUUID(MD5(concat('device',toString(number%20000)))),
		 reinterpretAsUUID(MD5(concat('session',toString(intDiv(number,50))))),
		 toDateTime64(?,9)+toIntervalSecond(intDiv(number*2678400,?)),
		 toDateTime(?)+toIntervalSecond(intDiv(number*2678400,?)),
		 reinterpretAsUUID(MD5(concat(?,'current',toString(number)))),
		 'exception',17,toUInt8(number%11=0),'Synthetic update error',
		 reinterpretAsUUID(MD5(concat('current-error',toString(99-number%100)))),
		 'ios','runtime-perf' FROM numbers(?)
		 SETTINGS max_partitions_per_insert_block=1000`,
		appID, updateID, currentFrom, currentCount, currentFrom, currentCount, appID, currentCount))
	require.NoError(t, engine.Conn.Exec(ctx, `INSERT INTO observe_logs
		(app_id,update_id,eas_client_id,session_id,timestamp,ingested_at,content_key,
		 event_name,severity_number,is_fatal,body,error_fingerprint,platform,runtime_version)
		SELECT toUUID(?),reinterpretAsUUID(MD5(concat(?,'history-update',toString(number%64)))),
		 reinterpretAsUUID(MD5(concat('device',toString(number%20000)))),
		 reinterpretAsUUID(MD5(concat('session',toString(intDiv(number,50))))),
		 toDateTime64(?,9)+toIntervalSecond(intDiv(number*31536000,?)),
		 toDateTime(?)+toIntervalSecond(intDiv(number*31536000,?)),
		 reinterpretAsUUID(MD5(concat(?,'historical',toString(number)))),
		 'exception',17,toUInt8(number%11=0),'Synthetic historical error',
		 reinterpretAsUUID(MD5(concat(if(intDiv(number,64)%1000<25,'current-error','historical-error'),
		 toString(intDiv(number,64)%1000)))),'ios','runtime-perf' FROM numbers(?)
		 SETTINGS max_partitions_per_insert_block=1000`,
		appID, appID, historicalFrom, historicalCount, historicalFrom, historicalCount, appID, historicalCount))
	require.NoError(t, engine.Conn.Exec(ctx, `INSERT INTO error_groups
		(app_id,update_id,error_fingerprint,group_fingerprint,error_type,message,culprit,trace,symbolicated_at)
		SELECT app_id,update_id,error_fingerprint,error_fingerprint,'TypeError',
		 'Synthetic canonical error','Screen.tsx in render','{}',now()
		 FROM observe_logs WHERE app_id=? GROUP BY app_id,update_id,error_fingerprint`, appID))
	var logs, aggregateRows, currentAggregateRows, ingestionHours, mappings uint64
	require.NoError(t, engine.Conn.QueryRow(ctx, "SELECT count() FROM observe_logs WHERE app_id=?", appID).Scan(&logs))
	require.NoError(t, engine.Conn.QueryRow(ctx, `SELECT count(),countIf(update_id=?),uniqExact(hour)
		 FROM error_occurrences WHERE app_id=?`, updateID, appID).Scan(&aggregateRows, &currentAggregateRows, &ingestionHours))
	require.NoError(t, engine.Conn.QueryRow(ctx, "SELECT count() FROM error_groups WHERE app_id=?", appID).Scan(&mappings))
	t.Logf("fixture app=%s update=%s raw_logs=%d aggregate_rows=%d current_aggregate_rows=%d ingestion_hours=%d mappings=%d history_days=365 current_days=31",
		appID, updateID, logs, aggregateRows, currentAggregateRows, ingestionHours, mappings)
	require.Equal(t, count, logs)
	tracked := &errorsMeasuredConn{Conn: engine.Conn}
	explorer := &Explorer{clickhouse: &clickhouse.Engine{Conn: tracked}}
	for _, limit := range []int{1, 25, 100} {
		tracked.calls = nil
		started := time.Now()
		page, err := explorer.ReadUpdateErrors(ctx, appID, updateID, limit, 0)
		require.NoError(t, err)
		t.Logf("update_errors limit=%d wall_ms=%d queries=%d", limit, time.Since(started).Milliseconds(), len(tracked.calls))
		require.Len(t, page.Errors, limit)
		require.Len(t, tracked.calls, 2, "summary and introduction lookups must not grow with page size")
		if limit == 100 {
			var occurrences uint64
			newGroups, recurringGroups := 0, 0
			for _, summary := range page.Errors {
				occurrences += summary.Occurrences
				require.NotNil(t, summary.New)
				if *summary.New {
					newGroups++
				} else {
					recurringGroups++
				}
			}
			require.Equal(t, currentCount, occurrences)
			require.Equal(t, 75, newGroups)
			require.Equal(t, 25, recurringGroups)
		}
		started = time.Now()
		cached, err := explorer.ReadUpdateErrors(ctx, appID, updateID, limit, 0)
		require.NoError(t, err)
		require.Equal(t, page, cached)
		require.Len(t, tracked.calls, 2, "an identical cached read must add no queries")
		t.Logf("update_errors cache_hit limit=%d wall_us=%d queries=0", limit, time.Since(started).Microseconds())
		for _, call := range tracked.calls {
			reportErrorsQuery(t, engine.Conn, call)
		}
	}
}
