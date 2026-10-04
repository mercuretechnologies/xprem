// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"testing"
	"time"
	"xprem/ee/identity"
	"xprem/internal/database/clickhouse"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestReadLogsPreservesFractionalWindowAndCursor(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)
	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()
	explorer := &Explorer{clickhouse: engine}
	app, update, device := uuid.NewString(), uuid.NewString(), uuid.NewString()
	from := time.Now().UTC().Truncate(time.Second).Add(-time.Minute).Add(100 * time.Millisecond)
	to := from.Add(10*time.Second + 887*time.Millisecond + 456*time.Nanosecond)
	var fixture []LogRow
	for i := 0; i < 210; i++ {
		fixture = append(fixture, errorLogRow(app, update, device, errorAttributes("Error", "boom", ""), 17, false, to))
	}
	for _, at := range []time.Time{from, from.Add(-time.Nanosecond), to.Add(time.Nanosecond)} {
		fixture = append(fixture, errorLogRow(app, update, device, errorAttributes("Error", "edge", ""), 17, false, at))
	}
	require.NoError(t, NewClickHouseTelemetrySink(engine).InsertLogs(ctx, append(fixture, fixture[0])))
	native, err := engine.Conn.PrepareBatch(ctx, `INSERT INTO device_health_events
		(app_id, update_id, eas_client_id, outbox_id, failure_type, occurred_at)`)
	require.NoError(t, err)
	defer native.Close()
	for i, at := range []time.Time{to, to, to, from.Add(-time.Millisecond), to.Add(time.Millisecond)} {
		require.NoError(t, native.Append(app, update, device, uint64(1000+i), string(identity.FailureTypeUpdate), at))
	}
	require.NoError(t, native.Send())
	t.Run("inclusive window", func(t *testing.T) {
		page, err := explorer.ReadLogs(ctx, app, LogsQuery{ExplorerQuery: ExplorerQuery{From: from, To: to}, Limit: 500})
		require.NoError(t, err)
		require.Len(t, page.Logs, 214, "exact edge events are included, outside edges and retries are excluded")
		foundTarget := false
		for _, row := range page.Logs {
			require.False(t, row.Timestamp.Before(from))
			require.False(t, row.Timestamp.After(to))
			foundTarget = foundTarget || row.EventKey == fixture[0].ContentKey.String()
		}
		require.True(t, foundTarget, "an occurrence link's exact upper bound retains its target")
	})
	for _, test := range []struct {
		name       string
		eventNames []string
		limit      int
		count      int
	}{
		{name: "mixed cursor beyond 200", limit: 200, count: 218},
		{name: "native cursor", eventNames: []string{nativeCrashEventName}, limit: 1, count: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Whole-second window edges isolate cursor precision from bound precision.
			query := LogsQuery{ExplorerQuery: ExplorerQuery{From: from.Truncate(time.Second).Add(-time.Second), To: to.Truncate(time.Second).Add(2 * time.Second)}, EventNames: test.eventNames, Limit: test.limit}
			seen := map[string]bool{}
			for {
				page, err := explorer.ReadLogs(ctx, app, query)
				require.NoError(t, err)
				for _, row := range page.Logs {
					require.False(t, seen[row.EventKey], "cursor pages do not repeat events")
					seen[row.EventKey] = true
				}
				if page.NextCursor == "" {
					break
				}
				query.Cursor, err = DecodeLogCursor(page.NextCursor)
				require.NoError(t, err)
			}
			require.Len(t, seen, test.count, "equal subsecond timestamps page without dropping events")
		})
	}
}
