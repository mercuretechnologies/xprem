// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
	"xprem/ee/symbolication"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xprem/internal/database/clickhouse"
)

func TestErrorsQueryBoundsAndIDs(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, duration := range []time.Duration{0, -time.Hour, ErrorsMaxWindow + time.Nanosecond} {
		_, err := normalizeErrorsQuery(ExplorerQuery{From: now, To: now.Add(duration)})
		require.ErrorIs(t, err, ErrInvalidErrorsQuery)
	}
	query, err := normalizeErrorsQuery(ExplorerQuery{From: now, To: now.Add(ErrorsMaxWindow), Bucket: time.Nanosecond})
	require.NoError(t, err)
	require.LessOrEqual(t, len(emptyErrorSeries(query)), 121)
	group := "g:" + uuid.NewString()
	decoded, err := decodeErrorID(encodeErrorID(group))
	require.NoError(t, err)
	require.Equal(t, group, decoded)
	for _, id := range []string{"not-an-id", encodeErrorID("g:" + ZeroUpdateID), encodeErrorID("f:[]")} {
		_, err = decodeErrorID(id)
		require.ErrorIs(t, err, ErrInvalidErrorID)
	}
	page, err := (*Explorer)(nil).ReadErrors(context.Background(), uuid.NewString(), ErrorsQuery{ExplorerQuery: query})
	require.NoError(t, err)
	require.False(t, page.Available)
	require.NotNil(t, page.Errors)
	details, err := (*Explorer)(nil).ReadErrorDetails(context.Background(), uuid.NewString(), encodeErrorID(group), ErrorDetailsQuery{ExplorerQuery: query})
	require.NoError(t, err)
	require.False(t, details.Available)
}

func TestErrorsBreakdownReconcilesOtherAndUnknown(t *testing.T) {
	segments := []ErrorBreakdown{{Occurrences: 100}, {Key: "B", Label: "B", Occurrences: 50}}
	segments = completeErrorBreakdown(segments, 200)
	require.Len(t, segments, 3)
	assert.Equal(t, "Unknown", segments[0].Label)
	assert.Equal(t, 50.0, segments[0].Percentage)
	assert.Equal(t, "Other", segments[2].Label)
	assert.EqualValues(t, 50, segments[2].Occurrences)
}

func TestErrorDetailsCacheKeyUsesCursorValues(t *testing.T) {
	first := LogCursor{Timestamp: time.Date(2026, 10, 1, 0, 0, 0, 123, time.UTC), EventKey: uuid.NewString()}
	second := first
	query := ErrorDetailsQuery{Cursor: &first, Limit: 50}
	key := errorDetailsReadCacheKey("app", "error", query)
	query.Cursor = &second
	require.Equal(t, key, errorDetailsReadCacheKey("app", "error", query), "separate allocations of the same cursor share a cache key")
	second.EventKey = uuid.NewString()
	require.NotEqual(t, key, errorDetailsReadCacheKey("app", "error", query), "changing a cursor at the same address must invalidate the key")
	query.Cursor = nil
	require.NotEqual(t, key, errorDetailsReadCacheKey("app", "error", query))
}

func TestGlobalErrorDetailsValidatesSnapshotAndCursor(t *testing.T) {
	id := encodeErrorID("g:" + uuid.NewString())
	cursor := &LogCursor{Timestamp: time.Now().UTC(), EventKey: uuid.NewString()}
	for _, query := range []ErrorDetailsQuery{
		{Global: true, Cursor: cursor},
		{Global: true, AsOf: time.Now().Add(time.Hour)},
		{Global: true, AsOf: time.Unix(-1, 0)},
		{Global: true, AsOf: time.Now().UTC(), Cursor: &LogCursor{EventKey: uuid.NewString()}},
		{Global: true, Limit: 101},
	} {
		_, err := (*Explorer)(nil).ReadErrorDetails(context.Background(), uuid.NewString(), id, query)
		require.ErrorIs(t, err, ErrInvalidErrorsQuery)
	}
	zone := time.FixedZone("UTC+1", 3600)
	asOf := time.Now().Add(-time.Minute).In(zone)
	details, err := (*Explorer)(nil).ReadErrorDetails(context.Background(), uuid.NewString(), id, ErrorDetailsQuery{Global: true, AsOf: asOf})
	require.NoError(t, err)
	require.NotNil(t, details.AsOf)
	require.Equal(t, asOf.UTC().Truncate(time.Second), *details.AsOf)
}

func TestGlobalErrorDetailsReadsRetainedGroupWithStableSnapshot(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)
	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()
	explorer := &Explorer{clickhouse: engine}
	app, otherApp, updateA, updateB, deviceA, deviceB, group := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	attrs := map[string]any{"exception.type": "Error", "exception.message": "render crash"}
	fixture := []LogRow{
		errorLogRow(app, updateA, deviceA, attrs, 17, false, now.Add(-400*24*time.Hour)),
		errorLogRow(app, updateA, deviceB, attrs, 21, true, now.Add(-time.Hour)),
		// All retained events count, including an SDK clock ahead of the server.
		errorLogRow(app, updateB, deviceA, attrs, 17, false, now.Add(time.Hour)),
	}
	for i := range fixture {
		fixture[i].Platform = "ios"
		fixture[i].DeviceModel = fmt.Sprintf("Phone-%d", i%2)
		fixture[i].RuntimeVersion = fmt.Sprintf("runtime-%d", i%2)
	}
	require.NoError(t, NewClickHouseTelemetrySink(engine).InsertLogs(ctx, append(fixture, fixture[0])))
	foreign := fixture[2]
	foreign.AppID, foreign.ContentKey, foreign.Timestamp = otherApp, uuid.New(), now.Add(2*time.Hour)
	require.NoError(t, NewClickHouseTelemetrySink(engine).InsertLogs(ctx, []LogRow{foreign}))
	// Place fixtures before the ingestion snapshot's second boundary.
	require.NoError(t, engine.Conn.Exec(ctx, `INSERT INTO observe_logs
		SELECT * REPLACE (toDateTime(?) AS ingested_at) FROM observe_logs WHERE app_id IN (?, ?)`, now.Add(-time.Minute), app, otherApp))
	require.NoError(t, explorer.writeErrorGroups(ctx, []groupedError{{
		errorKey:   errorKey{appID: otherApp, updateID: updateB, fingerprint: foreign.ErrorFingerprint.String()},
		ErrorGroup: ErrorGroup{GroupFingerprint: group, ErrorType: "Error", Message: "other app", SymbolicatedAt: now.Add(-time.Hour)},
	}}))
	for _, update := range []string{updateA, updateB} {
		require.NoError(t, explorer.writeErrorGroups(ctx, []groupedError{{
			errorKey:   errorKey{appID: app, updateID: update, fingerprint: fixture[0].ErrorFingerprint.String()},
			ErrorGroup: ErrorGroup{GroupFingerprint: group, ErrorType: "Error", Message: "render crash", SymbolicatedAt: now.Add(-time.Hour)},
		}}))
	}
	oldKey, err := json.Marshal([]string{updateA, fixture[0].ErrorFingerprint.String(), "", "", "", "", "", "v1"})
	require.NoError(t, err)
	oldID := encodeErrorID("f:" + string(oldKey))
	tracked := &errorsMeasuredConn{Conn: engine.Conn}
	explorer.clickhouse = &clickhouse.Engine{Conn: tracked}
	asOf := time.Now().UTC().Truncate(time.Second)
	query := ErrorDetailsQuery{
		Global: true, AsOf: asOf, Limit: 2, Fatality: "invalid",
		ExplorerQuery: ExplorerQuery{From: now, To: now, Platform: []string{"android"}, UpdateIDs: []string{uuid.NewString()}, DeviceModels: []string{"never"}},
	}
	details, err := explorer.readErrorDetails(ctx, app, oldID, query)
	require.NoError(t, err)
	require.NotNil(t, details.Summary)
	require.Equal(t, encodeErrorID("g:"+group), details.Summary.ErrorID, "an old fallback resolves without inherited filters")
	require.EqualValues(t, 3, details.Summary.Occurrences, "retries count once and events older than 31 days remain included")
	require.EqualValues(t, 2, details.Summary.ImpactedDevices)
	require.EqualValues(t, 1, details.Summary.CrashOccurrences)
	require.True(t, details.Summary.FirstSeen.Equal(fixture[0].Timestamp))
	require.True(t, details.Summary.LastSeen.Equal(fixture[2].Timestamp))
	require.NotNil(t, details.AsOf)
	require.True(t, details.AsOf.Equal(asOf))
	require.LessOrEqual(t, len(details.Series), maxErrorsBuckets)
	var seriesTotal uint64
	for _, point := range details.Series {
		seriesTotal += point.Count
	}
	require.Equal(t, details.Summary.Occurrences, seriesTotal)
	require.Len(t, details.Updates, 2)
	require.Len(t, details.DeviceModels, 2)
	require.Len(t, details.Runtimes, 2)
	require.Len(t, details.Occurrences, 2)
	require.NotEmpty(t, details.NextCursor)
	require.Equal(t, fixture[2].ContentKey.String(), details.RepresentativeOccurrence.EventKey, "the globally latest event is selected by default")
	require.Len(t, tracked.calls, 4, "fallback resolution plus one extent, one aggregate and one bounded payload query")
	for _, call := range tracked.calls[1:] {
		require.Contains(t, call.sql, "l.ingested_at < fromUnixTimestamp64Nano(?)")
		require.Contains(t, call.sql, "group_fingerprint IN ?", "raw scans are narrowed to candidate group keys")
	}

	windowed, err := explorer.readErrorDetails(ctx, app, details.Summary.ErrorID, ErrorDetailsQuery{
		ExplorerQuery: ExplorerQuery{From: now.Add(-2 * time.Hour), To: now, Platform: []string{"ios"}}, Fatality: "fatal",
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, windowed.Summary.Occurrences)
	require.Nil(t, windowed.AsOf)

	// A backdated arrival in the current second must not change later pages.
	require.NoError(t, engine.Conn.Exec(ctx, `INSERT INTO observe_logs
		(app_id, update_id, eas_client_id, session_id, timestamp, ingested_at, content_key,
		 event_name, severity_number, attributes, error_fingerprint)
		VALUES (?, ?, ?, ?, ?, fromUnixTimestamp64Nano(?), ?, 'js.exception', 17, ?, ?)`,
		app, updateA, deviceB, uuid.NewString(), now.Add(-100*24*time.Hour), asOf.UnixNano(),
		uuid.NewString(), fixture[0].Attributes, fixture[0].ErrorFingerprint))
	// The snapshot freezes ingestion, not mapping versions.
	require.NoError(t, explorer.writeErrorGroups(ctx, []groupedError{{
		errorKey:   errorKey{appID: app, updateID: updateB, fingerprint: fixture[0].ErrorFingerprint.String()},
		ErrorGroup: ErrorGroup{GroupFingerprint: group, ErrorType: "Error", Message: "updated title", SymbolicatedAt: asOf.Add(time.Hour)},
	}}))
	require.NoError(t, engine.Conn.Exec(ctx, "OPTIMIZE TABLE error_groups FINAL"))
	cursor, err := DecodeLogCursor(details.NextCursor)
	require.NoError(t, err)
	query.Cursor = cursor
	second, err := explorer.readErrorDetails(ctx, app, oldID, query)
	require.NoError(t, err)
	require.NotNil(t, second.Summary)
	require.Equal(t, details.Summary.Occurrences, second.Summary.Occurrences)
	require.Equal(t, "updated title", second.Summary.Message)
	require.Len(t, second.Occurrences, 1)
	require.Equal(t, fixture[0].ContentKey.String(), second.Occurrences[0].EventKey)
	require.Empty(t, second.NextCursor)
	unknown, err := explorer.readErrorDetails(ctx, app, encodeErrorID("g:"+uuid.NewString()), ErrorDetailsQuery{Global: true})
	require.NoError(t, err)
	require.Nil(t, unknown.Summary)
	require.Empty(t, unknown.Series)
	require.NotNil(t, unknown.AsOf)
	singleton, err := explorer.readErrorDetails(ctx, otherApp, encodeErrorID("g:"+group), ErrorDetailsQuery{Global: true, AsOf: asOf})
	require.NoError(t, err)
	require.EqualValues(t, 1, singleton.Summary.Occurrences, "identical group/update/raw keys in another app remain isolated")
	require.Equal(t, foreign.ContentKey.String(), singleton.RepresentativeOccurrence.EventKey)
	require.Len(t, singleton.Series, 2)
	require.EqualValues(t, 1, singleton.Series[0].Count)
	require.GreaterOrEqual(t, singleton.To.Sub(singleton.From), time.Second, "a singleton has a usable chart domain")
}

func TestErrorsLiveReconcilesCountsFiltersAndCursor(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)
	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()
	explorer := &Explorer{clickhouse: engine}
	app, updateA, updateB, deviceA, deviceB := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	attrs := map[string]any{"exception.type": "TypeError", "exception.message": "Cannot read name"}
	fixture := []LogRow{
		errorLogRow(app, updateA, deviceA, attrs, 17, false, now.Add(-3*time.Hour)),
		errorLogRow(app, updateA, deviceB, attrs, 21, true, now),
		errorLogRow(app, updateB, deviceA, attrs, 17, false, now),
	}
	for i := range fixture {
		fixture[i].DeviceModel = "Phone"
		fixture[i].OSName = "iOS"
		fixture[i].OSVersion = "18"
		fixture[i].Platform = "ios"
		fixture[i].RuntimeVersion = "1"
	}
	fixture[2].DeviceModel = "Tablet"
	require.NoError(t, NewClickHouseTelemetrySink(engine).InsertLogs(ctx, append(fixture, fixture[0])))
	base := ExplorerQuery{From: now.Add(-24 * time.Hour), To: now.Add(time.Hour), Bucket: time.Hour}
	before, err := explorer.ReadErrors(ctx, app, ErrorsQuery{ExplorerQuery: base, IncludeSeries: true})
	require.NoError(t, err)
	require.Len(t, before.Errors, 2)
	oldID := before.Errors[0].ErrorID
	group := uuid.NewString()
	require.NoError(t, explorer.writeErrorGroups(ctx, []groupedError{
		{errorKey: errorKey{appID: app, updateID: updateA, fingerprint: fixture[0].ErrorFingerprint.String()}, ErrorGroup: ErrorGroup{GroupFingerprint: group, ErrorType: "TypeError", Message: "Cannot read name", Culprit: "Screen.tsx", SymbolicatedAt: now}},
		{errorKey: errorKey{appID: app, updateID: updateB, fingerprint: fixture[0].ErrorFingerprint.String()}, ErrorGroup: ErrorGroup{GroupFingerprint: group, ErrorType: "TypeError", Message: "Changed title", Culprit: "Screen.tsx", SymbolicatedAt: now}},
	}))
	page, err := explorer.ReadErrors(ctx, app, ErrorsQuery{ExplorerQuery: base, IncludeSeries: true, Search: "Cannot read"})
	require.NoError(t, err)
	require.Len(t, page.Errors, 1)
	summary := page.Errors[0]
	assert.EqualValues(t, 3, summary.Occurrences)
	assert.EqualValues(t, 2, summary.ImpactedDevices)
	assert.EqualValues(t, 1, summary.CrashOccurrences)
	assert.True(t, summary.FirstSeen.Equal(fixture[0].Timestamp))
	var seriesCount uint64
	for _, point := range summary.Series {
		seriesCount += point.Count
	}
	assert.Equal(t, summary.Occurrences, seriesCount)
	details, err := explorer.ReadErrorDetails(ctx, app, oldID, ErrorDetailsQuery{ExplorerQuery: base, Limit: 1})
	require.NoError(t, err)
	require.NotNil(t, details.Summary)
	assert.Equal(t, summary.ErrorID, details.Summary.ErrorID, "old fallback resolves after symbolication")
	assert.Equal(t, summary.Occurrences, details.Summary.Occurrences)
	assert.Equal(t, summary.ImpactedDevices, details.Summary.ImpactedDevices)
	require.Len(t, details.Updates, 2)
	assert.InDelta(t, 66.6667, details.Updates[0].Percentage, 0.001)
	require.Len(t, details.Occurrences, 1)
	require.NotEmpty(t, details.NextCursor)
	seen := map[string]bool{}
	for {
		for _, row := range details.Occurrences {
			require.False(t, seen[row.EventKey])
			seen[row.EventKey] = true
			require.NotEmpty(t, row.ErrorFingerprint)
		}
		if details.NextCursor == "" {
			break
		}
		cursor, err := DecodeLogCursor(details.NextCursor)
		require.NoError(t, err)
		details, err = explorer.ReadErrorDetails(ctx, app, summary.ErrorID, ErrorDetailsQuery{ExplorerQuery: base, Cursor: cursor, Limit: 1})
		require.NoError(t, err)
	}
	assert.Len(t, seen, 3, "equal timestamps page without dropped/repeated events")
	filtered := base
	filtered.DeviceModels = []string{"Tablet"}
	filtered.OSNames = []string{"iOS"}
	filtered.RuntimeVersions = []string{"1"}
	details, err = explorer.ReadErrorDetails(ctx, app, summary.ErrorID, ErrorDetailsQuery{ExplorerQuery: filtered})
	require.NoError(t, err)
	require.NotNil(t, details.Summary)
	assert.EqualValues(t, 1, details.Summary.Occurrences)
	assert.Len(t, details.Occurrences, 1)
	assert.Len(t, details.DeviceModels, 1)
	fatal, err := explorer.ReadErrors(ctx, app, ErrorsQuery{ExplorerQuery: base, Fatality: "fatal"})
	require.NoError(t, err)
	require.Len(t, fatal.Errors, 1)
	assert.EqualValues(t, 1, fatal.Errors[0].Occurrences)
}

func TestErrorsSweepRecoversOlderPendingGroups(t *testing.T) {
	ctx := context.Background()
	explorer := isolatedErrorGroupsExplorer(t)
	engine := explorer.clickhouse

	var indexBytes bytes.Buffer
	require.NoError(t, symbolication.WriteIndex(&indexBytes, &symbolication.Map{
		Sources:        []string{"src/LabScreen.tsx"},
		SourcesContent: []string{"const onPress = () => {\n  throw new Error('Checkout failed')\n  submitCheckout()\n}\n"},
		Names:          []string{"onPress"},
		Ignored:        []bool{false},
		Segments: []symbolication.Segment{
			{Column: 100, Source: 0, OriginalLine: 1, OriginalColumn: 2, Name: 0},
			{Column: 200, Source: 0, OriginalLine: 2, OriginalColumn: 2, Name: 0},
		},
	}))
	index, err := symbolication.OpenIndex(bytes.NewReader(indexBytes.Bytes()))
	require.NoError(t, err)
	crashAt := func(offset int) map[string]any {
		return errorAttributes("Error", "Checkout failed", fmt.Sprintf("Error: Checkout failed\n    at onPress (address at /data/app.bundle:1:%d)", offset))
	}
	app, recent, sixDays, nineDays, oldest, noMap := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	deviceA, deviceB := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	fixture := []LogRow{
		errorLogRow(app, recent, deviceA, crashAt(120), 17, false, now.Add(-3*time.Hour)),
		errorLogRow(app, recent, deviceA, crashAt(120), 21, true, now),
		errorLogRow(app, sixDays, deviceA, crashAt(120), 21, true, now.Add(-6*24*time.Hour)),
		errorLogRow(app, nineDays, deviceB, crashAt(120), 17, false, now.Add(-9*24*time.Hour)),
		// The ingestion hour begins before the oldest permitted event time.
		errorLogRow(app, oldest, deviceA, crashAt(120), 21, true, now.Add(-ErrorsMaxWindow+time.Minute)),
		// An identical message at a different throw site must stay separate.
		errorLogRow(app, nineDays, deviceA, crashAt(220), 17, false, now.Add(-9*24*time.Hour)),
		errorLogRow(app, noMap, deviceA, crashAt(120), 21, true, now.Add(-6*24*time.Hour)),
	}
	// Set old ingestion times to exercise the sweep's lookback.
	batch, err := engine.Conn.PrepareBatch(ctx, `INSERT INTO observe_logs
		(app_id, update_id, eas_client_id, session_id, timestamp, ingested_at,
		 content_key, event_name, severity_number, is_fatal, attributes, error_fingerprint)`)
	require.NoError(t, err)
	defer batch.Close()
	for _, row := range append(fixture, fixture[0]) {
		require.NoError(t, batch.Append(row.AppID, row.UpdateID, row.EASClientID, row.SessionID,
			row.Timestamp, row.Timestamp, row.ContentKey, row.EventName, row.SeverityNumber,
			row.IsFatal, row.Attributes, row.ErrorFingerprint))
	}
	require.NoError(t, batch.Send())
	trace := symbolication.Symbolicate(index, exceptionOf(fixture[0].EventName, fixture[0].Body, crashAt(120)).stacktrace)
	require.NoError(t, explorer.writeErrorGroups(ctx, []groupedError{{
		errorKey: errorKey{appID: app, updateID: recent, fingerprint: fixture[0].ErrorFingerprint.String()},
		ErrorGroup: ErrorGroup{GroupFingerprint: symbolication.GroupFingerprint("Error", "Checkout failed", trace).String(),
			ErrorType: "Error", Message: "Checkout failed", Culprit: symbolication.Culprit(trace), Trace: trace, SymbolicatedAt: now},
	}}))
	query := ExplorerQuery{From: now.Add(-ErrorsMaxWindow), To: now, Bucket: 24 * time.Hour}
	before, err := explorer.readErrors(ctx, app, ErrorsQuery{ExplorerQuery: query, IncludeSeries: true})
	require.NoError(t, err)
	require.Len(t, before.Errors, 6)
	var oldID string
	for _, summary := range before.Errors {
		if summary.FirstSeen.Equal(fixture[4].Timestamp) {
			oldID = summary.ErrorID
		}
	}
	require.NotEmpty(t, oldID)
	ready := false
	sweep := NewErrorGroupsSweep(explorer, indexOpenerFunc(func(_ context.Context, appID, update string) (*symbolication.Index, error) {
		if appID != app || update == noMap {
			return nil, symbolication.ErrNoSourcemap
		}
		if !ready {
			return nil, symbolication.ErrIndexNotReady
		}
		return index, nil
	}))
	sweep.now = func() time.Time { return now.Add(-errorGroupsRetryDelay) }
	require.NoError(t, sweep.Run(ctx))
	ready = true
	sweep.now = func() time.Time { return now }
	require.NoError(t, sweep.Run(ctx))
	page, err := explorer.readErrors(ctx, app, ErrorsQuery{ExplorerQuery: query, IncludeSeries: true})
	require.NoError(t, err)
	require.Len(t, page.Errors, 3, "pending and mapped occurrences join after their source map becomes available")
	summary := page.Errors[0]
	assert.Equal(t, ErrorGroupReady, summary.SymbolicationStatus)
	assert.EqualValues(t, 5, summary.Occurrences, "ingestion retries do not add occurrences")
	assert.EqualValues(t, 2, summary.ImpactedDevices)
	assert.EqualValues(t, 3, summary.CrashOccurrences)
	assert.True(t, summary.FirstSeen.Equal(fixture[4].Timestamp), "the oldest hourly bucket is recovered")
	var seriesCount uint64
	for _, point := range summary.Series {
		seriesCount += point.Count
	}
	assert.Equal(t, summary.Occurrences, seriesCount)
	statuses := map[ErrorGroupStatus]int{}
	for _, row := range page.Errors {
		statuses[row.SymbolicationStatus]++
	}
	assert.Equal(t, 2, statuses[ErrorGroupReady], "different throw sites remain separate")
	assert.Equal(t, 1, statuses[ErrorGroupNoSourcemap], "an error with no map is not merged by its message")
	details, err := explorer.readErrorDetails(ctx, app, oldID, ErrorDetailsQuery{ExplorerQuery: query})
	require.NoError(t, err)
	require.NotNil(t, details.Summary)
	assert.Equal(t, summary.ErrorID, details.Summary.ErrorID, "old links resolve to the recovered group")
	assert.Equal(t, summary.Occurrences, details.Summary.Occurrences)
	assert.Equal(t, summary.ImpactedDevices, details.Summary.ImpactedDevices)
	require.Len(t, details.Occurrences, 5)
	require.Len(t, details.Updates, 4)
}

func TestErrorsLiveLegacyCrashAndEmbeddedIdentity(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)
	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()
	explorer := &Explorer{clickhouse: engine}
	app, device := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	rows := []LogRow{}
	for _, runtime := range []string{"one", "two"} {
		row := errorLogRow(app, ZeroUpdateID, device, map[string]any{"message": "boom"}, 9, false, now)
		row.EventName = JSCrashEventName
		row.RuntimeVersion = runtime
		rows = append(rows, row)
	}
	require.NoError(t, NewClickHouseTelemetrySink(engine).InsertLogs(ctx, rows))
	query := ExplorerQuery{From: now.Add(-time.Hour), To: now.Add(time.Hour)}
	page, err := explorer.ReadErrors(ctx, app, ErrorsQuery{ExplorerQuery: query, Fatality: "fatal"})
	require.NoError(t, err)
	require.Len(t, page.Errors, 2)
	for _, row := range page.Errors {
		assert.EqualValues(t, 1, row.Occurrences)
		assert.EqualValues(t, 1, row.CrashOccurrences)
	}
	key, err := decodeErrorID(page.Errors[0].ErrorID)
	require.NoError(t, err)
	var parts []string
	require.NoError(t, json.Unmarshal([]byte(key[2:]), &parts))
	assert.NotEqual(t, ZeroUpdateID, parts[1])
	details, err := explorer.ReadErrorDetails(ctx, app, page.Errors[0].ErrorID, ErrorDetailsQuery{ExplorerQuery: query})
	require.NoError(t, err)
	require.NotNil(t, details.Summary)
	assert.EqualValues(t, 1, details.Summary.Occurrences)
}

func TestErrorsLiveFractionalWindowAndUnknownBeyondTopSegments(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)
	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()
	explorer := &Explorer{clickhouse: engine}
	app, update, device := uuid.NewString(), uuid.NewString(), uuid.NewString()
	from := time.Now().UTC().Truncate(time.Second).Add(-time.Hour).Add(900 * time.Millisecond)
	to := from.Add(10*time.Second + 200*time.Millisecond)
	var rows []LogRow
	attrs := map[string]any{"exception.type": "Error", "exception.message": "shared"}
	for i := 0; i < 122; i++ {
		row := errorLogRow(app, update, device, attrs, 17, false, to)
		row.DeviceModel = fmt.Sprintf("model-%03d", i)
		rows = append(rows, row)
	}
	// Unknown ranks below all known segments, yet must survive the top-50 limit.
	unknown := errorLogRow(app, update, ZeroUpdateID, attrs, 17, false, to)
	rows = append(rows, unknown)
	before := errorLogRow(app, update, device, attrs, 17, false, from.Add(-time.Nanosecond))
	after := errorLogRow(app, update, device, attrs, 17, false, to.Add(time.Nanosecond))
	require.NoError(t, NewClickHouseTelemetrySink(engine).InsertLogs(ctx, append(rows, before, after)))
	query := ExplorerQuery{From: from, To: to, Bucket: time.Second}
	page, err := explorer.ReadErrors(ctx, app, ErrorsQuery{ExplorerQuery: query, IncludeSeries: true})
	require.NoError(t, err)
	require.Len(t, page.Errors, 1)
	summary := page.Errors[0]
	require.EqualValues(t, 123, summary.Occurrences, "nanosecond edges excluded exactly")
	require.EqualValues(t, 1, summary.ImpactedDevices, "unknown installation is not a distinct device")
	var sum uint64
	for _, point := range summary.Series {
		sum += point.Count
	}
	require.Equal(t, summary.Occurrences, sum)
	require.EqualValues(t, 123, summary.Series[len(summary.Series)-1].Count)
	details, err := explorer.ReadErrorDetails(ctx, app, summary.ErrorID, ErrorDetailsQuery{ExplorerQuery: query})
	require.NoError(t, err)
	require.Len(t, details.DeviceModels, 51)
	require.Equal(t, "Unknown", details.DeviceModels[0].Label)
	require.Equal(t, "Other", details.DeviceModels[50].Label)
	sum = 0
	var percentage float64
	for _, segment := range details.DeviceModels {
		sum += segment.Occurrences
		percentage += segment.Percentage
	}
	assert.Equal(t, summary.Occurrences, sum)
	assert.InDelta(t, 100.0, percentage, 0.000001)
}

func TestErrorsEnrichUpdatesUsesAppScopeAndDeletedFallback(t *testing.T) {
	_, pool, app, update := newStateFixture(t)
	ctx := context.Background()
	group := uuid.NewString()
	_, err := pool.Exec(ctx, "UPDATE updates SET message=$1,publish_group=$2 WHERE update_uuid=$3", "Fix checkout", group, update)
	require.NoError(t, err)
	missing := uuid.NewString()
	segments := []ErrorBreakdown{{Key: update, UpdateID: update, Label: update}, {Key: missing, UpdateID: missing, Label: missing}}
	explorer := &Explorer{postgres: &database.Engine{Queries: pgdb.New(pool), DB: pool}}
	require.NoError(t, explorer.enrichErrorUpdates(ctx, app, segments))
	assert.Equal(t, "Fix checkout", segments[0].Label)
	assert.Equal(t, group, segments[0].UpdateGroupID)
	assert.Equal(t, missing, segments[1].Label)
	otherAppSegments := []ErrorBreakdown{{Key: update, UpdateID: update, Label: update}}
	require.NoError(t, explorer.enrichErrorUpdates(ctx, uuid.NewString(), otherAppSegments))
	assert.Equal(t, update, otherAppSegments[0].Label)
}

func TestErrorsLiveUpdateBreakdownCombinesLegacyPublishGroups(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)
	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()

	app, updateA, updateB, device := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	publishGroup, errorGroup := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	attrs := map[string]any{"exception.type": "Error", "exception.message": "shared across updates"}
	var fixture []LogRow
	for i, event := range []struct{ update, platform, publishGroup string }{
		{updateA, "ios", ""},
		{updateA, "ios", publishGroup},
		// The most recent occurrence lacks the newer publish-group metadata.
		{updateA, "ios", ""},
		{updateA, "android", publishGroup},
		{updateB, "ios", ""},
		{updateB, "ios", ""},
	} {
		row := errorLogRow(app, event.update, device, attrs, 17, false, now.Add(time.Duration(i)*time.Second))
		row.Platform, row.UpdateGroupID = event.platform, event.publishGroup
		fixture = append(fixture, row)
	}
	require.NoError(t, NewClickHouseTelemetrySink(engine).InsertLogs(ctx, append(fixture, fixture[0])))
	tracked := &errorsMeasuredConn{Conn: engine.Conn}
	explorer := &Explorer{clickhouse: &clickhouse.Engine{Conn: tracked}}
	for _, update := range []string{updateA, updateB} {
		require.NoError(t, explorer.writeErrorGroups(ctx, []groupedError{{
			errorKey:   errorKey{appID: app, updateID: update, fingerprint: fixture[0].ErrorFingerprint.String()},
			ErrorGroup: ErrorGroup{GroupFingerprint: errorGroup, ErrorType: "Error", Message: "shared across updates", SymbolicatedAt: now},
		}}))
	}

	// Deleted releases must retain publish groups from their events.
	details, err := explorer.readErrorDetails(ctx, app, encodeErrorID("g:"+errorGroup), ErrorDetailsQuery{
		ExplorerQuery: ExplorerQuery{From: now.Add(-time.Hour), To: now.Add(time.Hour)},
	})
	require.NoError(t, err)
	require.Len(t, tracked.calls, 2, "one aggregate and one occurrence query, regardless of the number of update segments")
	require.NotNil(t, details.Summary)
	require.EqualValues(t, 6, details.Summary.Occurrences)
	require.Len(t, details.Updates, 3, "publish-group backfill must not split the same update and platform")

	keys := make(map[string]bool)
	var occurrences uint64
	var percentage float64
	for _, segment := range details.Updates {
		require.False(t, keys[segment.Key], "each update/platform segment needs a unique client key")
		keys[segment.Key] = true
		occurrences += segment.Occurrences
		percentage += segment.Percentage
		switch {
		case segment.UpdateID == updateA && segment.Platform == "ios":
			assert.EqualValues(t, 3, segment.Occurrences)
			assert.Equal(t, 50.0, segment.Percentage)
			assert.Equal(t, publishGroup, segment.UpdateGroupID)
		case segment.UpdateID == updateA && segment.Platform == "android":
			assert.EqualValues(t, 1, segment.Occurrences)
			assert.InDelta(t, 100.0/6, segment.Percentage, 0.000001)
			assert.Equal(t, publishGroup, segment.UpdateGroupID)
		case segment.UpdateID == updateB && segment.Platform == "ios":
			assert.EqualValues(t, 2, segment.Occurrences)
			assert.InDelta(t, 100.0/3, segment.Percentage, 0.000001)
			assert.Empty(t, segment.UpdateGroupID)
		default:
			t.Fatalf("unexpected update segment: %+v", segment)
		}
	}
	assert.Equal(t, details.Summary.Occurrences, occurrences)
	assert.InDelta(t, 100, percentage, 0.000001)
}
