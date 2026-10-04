// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"xprem/internal/database/clickhouse"
)

func TestUpdateErrorsReaderValidationAndUnavailable(t *testing.T) {
	appID, updateID := uuid.NewString(), uuid.NewString()
	for _, explorer := range []*Explorer{nil, {}, {clickhouse: &clickhouse.Engine{}}} {
		page, err := explorer.ReadUpdateErrors(context.Background(), appID, updateID, 0, 0)
		require.NoError(t, err)
		require.False(t, page.Available)
		require.Equal(t, updateID, page.UpdateID)
		require.Equal(t, 25, page.Limit)
		require.NotNil(t, page.Errors)
	}
	for _, limits := range [][2]int{{-1, 0}, {101, 0}, {25, -1}, {25, 10001}} {
		_, err := (*Explorer)(nil).ReadUpdateErrors(context.Background(), appID, updateID, limits[0], limits[1])
		require.ErrorIs(t, err, ErrInvalidErrorsQuery)
	}
	for _, id := range []string{"invalid", ZeroUpdateID} {
		_, err := (*Explorer)(nil).ReadUpdateErrors(context.Background(), appID, id, 25, 0)
		require.ErrorIs(t, err, ErrInvalidErrorsQuery)
		_, err = (*Explorer)(nil).ReadUpdateErrors(context.Background(), id, updateID, 25, 0)
		require.ErrorIs(t, err, ErrInvalidErrorsQuery)
	}
	encoded, err := json.Marshal(UpdateErrorSummary{})
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"new":null`)
	require.NotContains(t, string(encoded), `"series"`)
}

func TestUpdateErrorsLiveCountsManualRenderCrash(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)
	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()
	explorer := &Explorer{clickhouse: engine}
	appID, updateID := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	// ErrorBoundary emits a fatal manual crash without expo.error.is_fatal.
	attributes := map[string]any{
		"name": "Error", "message": "Deliberate render crash from the observe lab",
		"stack": "Error: Deliberate render crash from the observe lab\n    at LabScreen (address at app.hbc:1:120)",
	}
	row := errorLogRow(appID, updateID, uuid.NewString(), attributes, 21, false, now.Add(-time.Minute))
	row.EventName = JSCrashEventName
	require.NoError(t, NewClickHouseTelemetrySink(engine).InsertLogs(ctx, []LogRow{row}))
	errors, err := explorer.readErrors(ctx, appID, ErrorsQuery{
		ExplorerQuery: ExplorerQuery{From: now.Add(-time.Hour), To: now.Add(time.Minute)},
	})
	require.NoError(t, err)
	require.Len(t, errors.Errors, 1)
	require.EqualValues(t, 1, errors.Errors[0].CrashOccurrences)
	page, err := explorer.readUpdateErrors(ctx, appID, updateID, 25, 0)
	require.NoError(t, err)
	require.Len(t, page.Errors, 1)
	require.Equal(t, errors.Errors[0].CrashOccurrences, page.Errors[0].CrashOccurrences)
}

func TestUpdateErrorsLiveManualCrashesRetainedDeduplicatedAndScoped(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)
	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()
	explorer := &Explorer{clickhouse: engine}
	appID, updateID, groupID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Second)
	attributes := map[string]any{"name": "Error", "message": "manual crash"}
	manual := errorLogRow(appID, updateID, uuid.NewString(), attributes, 21, false, now)
	manual.EventName = JSCrashEventName
	flagged := errorLogRow(appID, updateID, manual.EASClientID, attributes, 21, true, now.Add(-time.Minute))
	flagged.EventName = JSCrashEventName
	native := errorLogRow(appID, updateID, manual.EASClientID, attributes, 21, true, now.Add(-2*time.Minute))
	nonfatal := errorLogRow(appID, updateID, manual.EASClientID, attributes, 17, false, now.Add(-3*time.Minute))
	otherApp, otherUpdate := manual, manual
	otherApp.AppID, otherApp.ContentKey = uuid.NewString(), uuid.New()
	otherUpdate.UpdateID, otherUpdate.ContentKey = uuid.NewString(), uuid.New()
	require.NoError(t, NewClickHouseTelemetrySink(engine).InsertLogs(ctx,
		[]LogRow{manual, manual, flagged, native, nonfatal, otherApp, otherUpdate}))
	// Retained logs span multiple aggregate hours and raw fingerprints of a
	// canonical group. Joining their correction to every hourly state would
	// count the first fingerprint's manual crashes more than once.
	older := errorLogRow(appID, updateID, manual.EASClientID,
		map[string]any{"name": "TypeError", "message": "older manual crash"}, 21, false, now.Add(-401*24*time.Hour))
	older.EventName = JSCrashEventName
	for _, row := range []LogRow{manual, older} {
		at := now.Add(-400 * 24 * time.Hour)
		if row.ErrorFingerprint == older.ErrorFingerprint {
			at = older.Timestamp
		}
		require.NoError(t, engine.Conn.Exec(ctx, `INSERT INTO observe_logs
		 (app_id,update_id,eas_client_id,event_name,severity_number,is_fatal,attributes,
		 timestamp,ingested_at,content_key,error_fingerprint)
		 VALUES (?,?,?,?,?,0,?,?,?,?,?)`, appID, updateID, row.EASClientID, JSCrashEventName,
			21, row.Attributes, at, at, uuid.New(), row.ErrorFingerprint))
	}
	var mappings []groupedError
	for _, row := range []LogRow{manual, older} {
		mappings = append(mappings, groupedError{
			errorKey:   errorKey{appID: appID, updateID: updateID, fingerprint: row.ErrorFingerprint.String()},
			ErrorGroup: ErrorGroup{GroupFingerprint: groupID, ErrorType: "Error", Message: "manual crash", SymbolicatedAt: now},
		})
	}
	require.NoError(t, explorer.writeErrorGroups(ctx, mappings))
	page, err := explorer.readUpdateErrors(ctx, appID, updateID, 25, 0)
	require.NoError(t, err)
	require.Len(t, page.Errors, 1)
	require.Equal(t, encodeErrorID("g:"+groupID), page.Errors[0].ErrorID)
	require.EqualValues(t, 7, page.Errors[0].Occurrences, "aggregate occurrences still include the SDK retry")
	require.EqualValues(t, 5, page.Errors[0].CrashOccurrences,
		"three distinct manual crashes plus two fatal flags, without retry, nonfatal, or foreign rows")
}

func TestUpdateErrorsLiveGroupsAndDetectsIntroduction(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)
	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()
	explorer := &Explorer{clickhouse: engine}
	appID, otherApp, updateID, olderUpdate, newerUpdate := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	deviceA, deviceB := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	groupOld, groupNew, groupTie, groupStale, groupChanged := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	var fixture []LogRow
	var mappings []groupedError
	add := func(app, update, message, group, device string, timestamp time.Time, crash bool, count int) string {
		attributes := map[string]any{"exception.type": "TypeError", "exception.message": message}
		row := errorLogRow(app, update, device, attributes, 17, crash, timestamp)
		for range count {
			copy := row
			copy.ContentKey = uuid.New()
			fixture = append(fixture, copy)
		}
		if group != "" {
			mappings = append(mappings, groupedError{
				errorKey: errorKey{appID: app, updateID: update, fingerprint: row.ErrorFingerprint.String()},
				ErrorGroup: ErrorGroup{GroupFingerprint: group, ErrorType: "TypeError", Message: "canonical " + group,
					Culprit: "Screen.tsx in render", SymbolicatedAt: now.Add(-time.Minute)},
			})
			if group == ZeroUpdateID {
				mappings[len(mappings)-1].ErrorType = ""
				mappings[len(mappings)-1].Message = ""
				mappings[len(mappings)-1].Culprit = ""
			}
		}
		return row.ErrorFingerprint.String()
	}
	oldRaw := add(appID, updateID, "old raw", groupOld, deviceA, now, false, 2)
	add(appID, updateID, "old other raw", groupOld, deviceB, now.Add(time.Second), true, 2)
	add(appID, olderUpdate, "previous old raw", groupOld, deviceA, now.Add(-400*24*time.Hour), false, 1)
	add(appID, updateID, "new raw", groupNew, deviceA, now.Add(-time.Hour), false, 3)
	add(appID, newerUpdate, "newer raw", groupNew, deviceB, now, false, 1)
	add(otherApp, olderUpdate, "foreign raw", groupNew, deviceA, now.Add(-500*24*time.Hour), false, 1)
	add(appID, updateID, "tie raw", groupTie, deviceA, now, false, 2)
	add(appID, olderUpdate, "tie previous raw", groupTie, deviceA, now, false, 1)
	add(appID, updateID, "stale raw", groupStale, deviceA, now, false, 1)
	staleFingerprint := add(appID, olderUpdate, "stale previous raw", groupStale, deviceA, now.Add(-24*time.Hour), false, 1)
	// A historical stale mapping cannot make a group look pre-existing.
	mappings = append(mappings, groupedError{
		errorKey:   errorKey{appID: appID, updateID: olderUpdate, fingerprint: staleFingerprint},
		ErrorGroup: ErrorGroup{GroupFingerprint: groupChanged, SymbolicatedAt: now},
	})
	waitingRaw := add(appID, updateID, "waiting raw", "", deviceA, now, false, 1)
	noMapRaw := add(appID, updateID, "no map raw", ZeroUpdateID, deviceA, now, false, 1)
	// A later real mapping replaces an earlier no-map mark.
	mappings = append([]groupedError{{
		errorKey:   errorKey{appID: appID, updateID: updateID, fingerprint: oldRaw},
		ErrorGroup: ErrorGroup{GroupFingerprint: ZeroUpdateID, SymbolicatedAt: now.Add(-2 * time.Minute)},
	}}, mappings...)
	require.NoError(t, NewClickHouseTelemetrySink(engine).InsertLogs(ctx, fixture))
	require.NoError(t, explorer.writeErrorGroups(ctx, mappings))
	tracked := &errorsMeasuredConn{Conn: engine.Conn}
	explorer.clickhouse = &clickhouse.Engine{Conn: tracked}
	page, err := explorer.readUpdateErrors(ctx, appID, updateID, 25, 0)
	require.NoError(t, err)
	require.True(t, page.Available)
	require.Len(t, page.Errors, 6)
	require.False(t, page.HasMore)
	byID := make(map[string]UpdateErrorSummary, len(page.Errors))
	for _, summary := range page.Errors {
		byID[summary.ErrorID] = summary
		require.Empty(t, summary.Series)
	}
	old := byID[encodeErrorID("g:"+groupOld)]
	require.EqualValues(t, 4, old.Occurrences)
	require.EqualValues(t, 2, old.ImpactedDevices, "device states must merge across raw fingerprints of one canonical group")
	require.EqualValues(t, 2, old.CrashOccurrences)
	require.True(t, old.FirstSeen.Equal(now))
	require.True(t, old.LastSeen.Equal(now.Add(time.Second)))
	require.Equal(t, "canonical "+groupOld, old.Message)
	require.Equal(t, ErrorGroupReady, old.SymbolicationStatus)
	require.NotNil(t, old.New)
	require.False(t, *old.New)
	for _, group := range []string{groupNew, groupStale} {
		summary := byID[encodeErrorID("g:"+group)]
		require.NotNil(t, summary.New)
		require.True(t, *summary.New, "later history, another app, and stale mappings must not suppress New")
	}
	tie := byID[encodeErrorID("g:"+groupTie)]
	require.NotNil(t, tie.New)
	require.False(t, *tie.New, "a tie between updates is conservatively not new")
	for fingerprint, status := range map[string]ErrorGroupStatus{waitingRaw: ErrorGroupWaiting, noMapRaw: ErrorGroupNoSourcemap} {
		parts, err := json.Marshal([]string{updateID, fingerprint, "", "", "", "", "", "v1"})
		require.NoError(t, err)
		summary := byID[encodeErrorID("f:"+string(parts))]
		require.Nil(t, summary.New)
		require.Equal(t, status, summary.SymbolicationStatus)
		require.Contains(t, summary.Message, "raw")
	}
	require.Len(t, tracked.calls, 2, "one summary query and one paginated history query")
	require.Contains(t, tracked.calls[0].sql, "FROM observe_logs WHERE app_id = ? AND update_id = ?")
	require.Contains(t, tracked.calls[0].sql, "AND event_name = 'xprem_js_crash' AND is_fatal = 0")
	require.NotContains(t, tracked.calls[1].sql, "observe_logs")
	for _, call := range tracked.calls {
		require.NotContains(t, strings.ToLower(call.sql), "trace")
		require.NotContains(t, call.sql, "attributes")
	}
	require.Contains(t, tracked.calls[1].sql, "group_fingerprint IN ?")
	require.Contains(t, tracked.calls[1].sql, "(update_id, error_fingerprint) IN")

	tracked.calls = nil
	first, err := explorer.readUpdateErrors(ctx, appID, updateID, 2, 0)
	require.NoError(t, err)
	require.True(t, first.HasMore)
	require.Len(t, first.Errors, 2)
	require.Equal(t, page.Errors[:2], first.Errors)
	second, err := explorer.readUpdateErrors(ctx, appID, updateID, 2, 2)
	require.NoError(t, err)
	require.Equal(t, page.Errors[2:4], second.Errors)
	require.True(t, second.HasMore)
	last, err := explorer.readUpdateErrors(ctx, appID, updateID, 2, 4)
	require.NoError(t, err)
	require.Equal(t, page.Errors[4:], last.Errors)
	require.False(t, last.HasMore)
	beforeEmpty := len(tracked.calls)
	empty, err := explorer.readUpdateErrors(ctx, appID, uuid.NewString(), 25, 0)
	require.NoError(t, err)
	require.Empty(t, empty.Errors)
	require.Len(t, tracked.calls, beforeEmpty+1, "empty updates do not query history")

	tracked.calls = nil
	fresh, err := explorer.ReadUpdateErrors(ctx, appID, updateID, 3, 0)
	require.NoError(t, err)
	require.Len(t, tracked.calls, 2)
	cached, err := explorer.ReadUpdateErrors(ctx, appID, updateID, 3, 0)
	require.NoError(t, err)
	require.Equal(t, fresh, cached)
	require.Len(t, tracked.calls, 2, "repeated update reads share the aggregate cache")
}
