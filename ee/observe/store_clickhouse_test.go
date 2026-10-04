// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"
	"xprem/ee/symbolication"
	"xprem/internal/database/clickhouse"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Needs TEST_CLICKHOUSE_URL and TEST_DATABASE_URL to run.
func TestClickHouseTelemetrySinkRoundTrip(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)

	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()
	sink := NewClickHouseTelemetrySink(engine)

	appID := uuid.NewString()
	now := time.Now().UTC()

	metricBatch, err := DecodeMetrics(bytes.NewReader(loadFixture(t, "ios_metrics.json")))
	require.NoError(t, err)
	metricRows := FlattenMetrics(appID, metricBatch, now)
	require.NotEmpty(t, metricRows)
	for i := range metricRows {
		metricRows[i].Branch = "main"
	}
	require.NoError(t, sink.InsertMetrics(ctx, metricRows))

	logBatch, err := DecodeLogs(bytes.NewReader(loadFixture(t, "ios_logs.json")))
	require.NoError(t, err)
	logRows := FlattenLogs(appID, logBatch, now)
	require.NotEmpty(t, logRows)
	require.NoError(t, sink.InsertLogs(ctx, logRows))

	var metricCount, logCount uint64
	require.NoError(t, engine.Conn.QueryRow(ctx,
		"SELECT count() FROM observe_metrics WHERE app_id = ?", appID).Scan(&metricCount))
	require.EqualValues(t, len(metricRows), metricCount)
	require.NoError(t, engine.Conn.QueryRow(ctx,
		"SELECT count() FROM observe_logs WHERE app_id = ?", appID).Scan(&logCount))
	require.EqualValues(t, len(logRows), logCount)

	var branch, updateID, platform string
	var value float64
	require.NoError(t, engine.Conn.QueryRow(ctx, `
		SELECT branch, toString(update_id), platform, value
		FROM observe_metrics
		WHERE app_id = ? AND metric_name = 'expo.app_startup.tti'`, appID,
	).Scan(&branch, &updateID, &platform, &value))
	assert.Equal(t, "main", branch)
	assert.Equal(t, "9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10", updateID)
	assert.Equal(t, "ios", platform)
	assert.InDelta(t, 1.842, value, 0.0001)

	var fatalCount uint64
	require.NoError(t, engine.Conn.QueryRow(ctx, `
		SELECT count() FROM observe_logs
		WHERE app_id = ? AND event_name = 'exception' AND is_fatal = 1`, appID).Scan(&fatalCount))
	require.EqualValues(t, 1, fatalCount)

	require.NoError(t, sink.InsertLogs(ctx, logRows))
	var total, distinct uint64
	require.NoError(t, engine.Conn.QueryRow(ctx, `
		SELECT count(), uniqExact(content_key) FROM observe_logs WHERE app_id = ?`, appID,
	).Scan(&total, &distinct))
	assert.EqualValues(t, 2*len(logRows), total)
	assert.EqualValues(t, len(logRows), distinct)
}

func TestHealthHistoryRoundTripUsesLatestSnapshotInMinute(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)

	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()

	appID, updateID, emptyUpdateID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	bucket := time.Now().UTC().Truncate(time.Minute)
	batch, err := engine.Conn.PrepareBatch(ctx, `INSERT INTO update_health_snapshots
		(app_id, update_id, bucket, captured_at, role, devices_on_update,
		 successful_devices, faulty_devices, update_issues, runtime_issues)`)
	require.NoError(t, err)
	require.NoError(t, batch.Append(
		appID, updateID, bucket, bucket.Add(time.Second), "candidate",
		uint64(10), uint64(9), uint64(1), uint64(1), uint64(0),
	))
	require.NoError(t, batch.Append(
		appID, updateID, bucket, bucket.Add(2*time.Second), "candidate",
		uint64(20), uint64(18), uint64(2), uint64(1), uint64(1),
	))
	require.NoError(t, batch.Send())

	history := NewHealthHistory(nil, engine)
	points, err := history.Read(
		ctx,
		appID,
		[]string{updateID, emptyUpdateID},
		bucket.Add(-time.Minute),
		bucket.Add(time.Minute),
	)
	require.NoError(t, err)
	require.Len(t, points[updateID], 1)
	require.Empty(t, points[emptyUpdateID])
	assert.EqualValues(t, 20, points[updateID][0].DevicesOnUpdate)
	require.NotNil(t, points[updateID][0].HealthPercent)
	assert.InDelta(t, 90, *points[updateID][0].HealthPercent, 0.001)
}

// countedError is one row of error_occurrences, summed over its hours.
type countedError struct {
	fingerprint, title            string
	occurrences, crashes, devices uint64
	firstSeen, lastSeen           time.Time
}

func countedErrorsOf(t *testing.T, engine *clickhouse.Engine, appID, updateID string) []countedError {
	t.Helper()
	rows, err := engine.Conn.Query(context.Background(), `
		SELECT toString(error_fingerprint), any(title), sum(occurrences), sum(crashes),
		       uniqMerge(devices), min(first_seen), max(last_seen)
		FROM error_occurrences
		WHERE app_id = ? AND update_id = ?
		GROUP BY error_fingerprint
		ORDER BY sum(occurrences) DESC`, appID, updateID)
	require.NoError(t, err)
	defer rows.Close()
	var counted []countedError
	for rows.Next() {
		var row countedError
		require.NoError(t, rows.Scan(&row.fingerprint, &row.title, &row.occurrences, &row.crashes,
			&row.devices, &row.firstSeen, &row.lastSeen))
		counted = append(counted, row)
	}
	require.NoError(t, rows.Err())
	return counted
}

// Needs TEST_CLICKHOUSE_URL and TEST_DATABASE_URL to run.
func TestErrorOccurrencesCountEachErrorOfAnUpdate(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)

	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()

	appID, updateID, otherUpdateID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	firstDevice, secondDevice := uuid.NewString(), uuid.NewString()
	nullPointer := errorAttributes("TypeError", "Cannot read property 'name' of undefined", "")
	timeout := errorAttributes("Error", "Request timed out", "")
	manualCrash := map[string]any{"name": "RangeError", "message": "Maximum call stack size exceeded", "stack": ""}
	now := time.Now().UTC().Truncate(time.Millisecond)

	logRow := func(update, device string, attributes map[string]any, severity uint8, fatal bool, at time.Time) LogRow {
		return errorLogRow(appID, update, device, attributes, severity, fatal, at)
	}
	fingerprintOf := func(attributes map[string]any) string {
		return fingerprintFor(LogRow{SeverityNumber: severityError}, attributes).String()
	}
	require.NoError(t, NewClickHouseTelemetrySink(engine).InsertLogs(ctx, []LogRow{
		logRow(updateID, firstDevice, nullPointer, 21, true, now.Add(-2*time.Hour)),
		logRow(updateID, firstDevice, nullPointer, 21, true, now.Add(-time.Hour)),
		logRow(updateID, secondDevice, nullPointer, 17, false, now),
		logRow(updateID, secondDevice, timeout, 17, false, now),
		logRow(otherUpdateID, firstDevice, nullPointer, 21, true, now),
		logRow(updateID, firstDevice, manualCrash, 21, true, now),
		// An info log carries no fingerprint, whatever its attributes say.
		logRow(updateID, firstDevice, map[string]any{"message": "lab"}, 9, false, now),
	}))

	counted := countedErrorsOf(t, engine, appID, updateID)
	require.Len(t, counted, 3, "one entry per distinct error, the plain log counts for nothing")

	mostFrequent := counted[0]
	assert.Equal(t, fingerprintOf(nullPointer), mostFrequent.fingerprint)
	assert.Equal(t, "TypeError: Cannot read property 'name' of undefined", mostFrequent.title)
	assert.EqualValues(t, 3, mostFrequent.occurrences, "the other update's occurrence is not counted")
	assert.EqualValues(t, 2, mostFrequent.crashes)
	assert.EqualValues(t, 2, mostFrequent.devices)
	assert.True(t, mostFrequent.firstSeen.Equal(now.Add(-2*time.Hour)))
	assert.True(t, mostFrequent.lastSeen.Equal(now))

	titles := []string{counted[1].title, counted[2].title}
	assert.ElementsMatch(t, []string{"Error: Request timed out", "RangeError: Maximum call stack size exceeded"}, titles,
		"the manual event's name and message title it like the SDK's keys")

	// The default logs query unions native crashes in: both arms carry the fingerprint.
	page, err := (&Explorer{clickhouse: engine}).ReadLogs(ctx, appID, LogsQuery{
		ExplorerQuery: ExplorerQuery{From: now.Add(-3 * time.Hour), To: now.Add(time.Minute), UpdateIDs: []string{updateID}},
		Limit:         10,
	})
	require.NoError(t, err)
	fingerprints := map[string]int{}
	for _, row := range page.Logs {
		fingerprints[row.ErrorFingerprint]++
	}
	assert.Equal(t, 3, fingerprints[fingerprintOf(nullPointer)])
	assert.Equal(t, 1, fingerprints[fingerprintOf(manualCrash)])
	assert.Equal(t, 1, fingerprints[""], "the plain log has no fingerprint")
}

// A map of one bundle line where offset 120 is onPress in LabScreen.tsx.
func labIndex(t *testing.T) *symbolication.Index {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, symbolication.WriteIndex(&buf, &symbolication.Map{
		Sources:        []string{"src/LabScreen.tsx"},
		SourcesContent: []string{"const onPress = () => {\n  console.log(user.profile.name)\n}\n"},
		Names:          []string{"onPress"},
		Ignored:        []bool{false},
		Segments:       []symbolication.Segment{{Column: 100, Source: 0, OriginalLine: 1, OriginalColumn: 2, Name: 0}},
	}))
	index, err := symbolication.OpenIndex(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	return index
}

type indexOpenerFunc func(ctx context.Context, appID, updateUUID string) (*symbolication.Index, error)

func (f indexOpenerFunc) OpenUpdateIndex(ctx context.Context, appID, updateUUID string) (*symbolication.Index, error) {
	return f(ctx, appID, updateUUID)
}

// Needs TEST_CLICKHOUSE_URL and TEST_DATABASE_URL to run.
func TestErrorGroupsSweepGroupsEachErrorOnce(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)

	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()
	explorer := &Explorer{clickhouse: engine}

	appID, indexedUpdate, unmappedUpdate := uuid.NewString(), uuid.NewString(), uuid.NewString()
	bundle := "/data/.expo-internal/cc6bcf26.bundle"
	crash := errorAttributes("TypeError", "Cannot read property 'name' of undefined",
		"TypeError: Cannot read property 'name' of undefined\n    at onPress (address at "+bundle+":1:120)\n    at forEach (native)")
	now := time.Now().UTC()
	logRow := func(update string, attributes map[string]any) LogRow {
		return errorLogRow(appID, update, uuid.NewString(), attributes, 21, true, time.Now().UTC())
	}
	require.NoError(t, NewClickHouseTelemetrySink(engine).InsertLogs(ctx, []LogRow{
		logRow(indexedUpdate, crash), logRow(indexedUpdate, crash), logRow(unmappedUpdate, crash),
	}))

	// The sweep covers every app in the shared test database; only this one's opens count.
	opened := 0
	sweep := NewErrorGroupsSweep(explorer, indexOpenerFunc(func(_ context.Context, app, updateUUID string) (*symbolication.Index, error) {
		if app != appID {
			return nil, symbolication.ErrNoSourcemap
		}
		opened++
		if updateUUID == unmappedUpdate {
			return nil, symbolication.ErrNoSourcemap
		}
		return labIndex(t), nil
	}))
	require.NoError(t, sweep.Run(ctx))
	assert.Equal(t, 2, opened, "one open per update")

	fingerprint := fingerprintFor(LogRow{SeverityNumber: 21, IsFatal: true}, crash).String()
	group, err := explorer.ReadErrorGroup(ctx, appID, indexedUpdate, fingerprint)
	require.NoError(t, err)
	require.NotNil(t, group)
	assert.Equal(t, "TypeError", group.ErrorType)
	assert.Equal(t, "LabScreen.tsx in onPress", group.Culprit)
	require.Len(t, group.Trace.Frames, 2)
	require.NotNil(t, group.Trace.Frames[0].Origin)
	assert.Equal(t, 2, group.Trace.Frames[0].Origin.Line)
	assert.Equal(t, []string{"const onPress = () => {", "  console.log(user.profile.name)", "}"}, group.Trace.Frames[0].Origin.Context.Lines)

	unmapped, err := explorer.ReadErrorGroup(ctx, appID, unmappedUpdate, fingerprint)
	require.NoError(t, err)
	assert.Nil(t, unmapped, "an update without a map keeps its error ungrouped")

	for _, key := range pendingKeys(t, explorer, now.Add(-time.Hour), 1000) {
		assert.NotEqual(t, indexedUpdate, key.updateID, "a grouped error is not listed again")
		assert.NotEqual(t, unmappedUpdate, key.updateID, "a marked error is not listed again")
	}
}

func pendingKeys(t *testing.T, explorer *Explorer, since time.Time, limit int) []errorKey {
	t.Helper()
	pending, err := explorer.pendingErrorGroups(context.Background(), since, limit, 0)
	require.NoError(t, err)
	return pending
}

// Needs TEST_CLICKHOUSE_URL and TEST_DATABASE_URL to run.
func TestErrorGroupsSweepIsNotStarvedByErrorsItCannotGroup(t *testing.T) {
	chURL, pgURL := requireLiveStores(t)
	clickhouse.RunDBMigrations(chURL, pgURL)

	ctx := context.Background()
	engine, err := clickhouse.NewClickHouseEngine(ctx, chURL)
	require.NoError(t, err)
	defer engine.Close()
	explorer := &Explorer{clickhouse: engine}

	appID, indexedUpdate, unmappedUpdate, brokenUpdate := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	bundle := "/data/.expo-internal/cc6bcf26.bundle"
	crashAt := func(offset int) map[string]any {
		return errorAttributes("Error", "boom", fmt.Sprintf("Error: boom\n    at onPress (address at %s:1:%d)", bundle, offset))
	}
	logRow := func(update string, attributes map[string]any) LogRow {
		return errorLogRow(appID, update, uuid.NewString(), attributes, 21, true, time.Now().UTC())
	}
	var rows []LogRow
	for offset := 0; offset < errorGroupsPerSweep; offset++ {
		rows = append(rows,
			logRow(unmappedUpdate, crashAt(1000+offset)), logRow(unmappedUpdate, crashAt(1000+offset)),
			logRow(brokenUpdate, crashAt(1000+offset)), logRow(brokenUpdate, crashAt(1000+offset)))
	}
	mapped := crashAt(120)
	rows = append(rows, logRow(indexedUpdate, mapped))
	require.NoError(t, NewClickHouseTelemetrySink(engine).InsertLogs(ctx, rows))

	sweep := NewErrorGroupsSweep(explorer, indexOpenerFunc(func(_ context.Context, app, update string) (*symbolication.Index, error) {
		switch {
		case app != appID || update == unmappedUpdate:
			return nil, symbolication.ErrNoSourcemap
		case update == brokenUpdate:
			return nil, symbolication.ErrIndexFailed
		}
		return labIndex(t), nil
	}))
	require.NoError(t, sweep.Run(ctx))

	group, err := explorer.ReadErrorGroup(ctx, appID, indexedUpdate, fingerprintFor(LogRow{SeverityNumber: 21}, mapped).String())
	require.NoError(t, err)
	require.NotNil(t, group, "the error with a map is grouped in the same pass as the more frequent ones it cannot group")
	listed := map[string]bool{}
	for _, key := range pendingKeys(t, explorer, time.Now().Add(-time.Hour), 100000) {
		listed[key.updateID] = true
	}
	assert.False(t, listed[unmappedUpdate], "an error without a map is marked and not listed again")
	assert.True(t, listed[brokenUpdate], "an error with a failed index waits for a reindex")
}

// errorLogRow is a js.exception row as the sink stores it.
func errorLogRow(appID, update, device string, attrs map[string]any, severity uint8, fatal bool, at time.Time) LogRow {
	attributes, traces := marshalAttributes(attrs, nil)
	row := LogRow{
		Envelope: Envelope{
			AppID: appID, EASClientID: device, UpdateID: update, SessionID: uuid.NewString(),
			Attributes: attributes, Timestamp: at, ContentKey: uuid.New(),
		},
		EventName: "js.exception", SeverityNumber: severity, IsFatal: fatal,
	}
	row.ErrorFingerprint = errorFingerprint(row, attrs, traces)
	return row
}
