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
	for _, call := range tracked.calls {
		require.NotContains(t, call.sql, "observe_logs")
		require.NotContains(t, strings.ToLower(call.sql), "trace")
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
