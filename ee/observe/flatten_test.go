// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The testdata files are real wire payloads captured from the iOS and Android
// SDKs (pretty-printed JSON on Android, resource attribute sets per platform,
// uppercase UUIDs on iOS). now is fixed so clamp assertions are stable; the
// fixture timestamps are January 2026.
var flattenNow = time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return body
}

func TestFlattenMetricsIOS(t *testing.T) {
	batch, err := DecodeMetrics(bytes.NewReader(loadFixture(t, "ios_metrics.json")))
	require.NoError(t, err)
	rows := FlattenMetrics("app-1", batch, flattenNow)
	require.Len(t, rows, 4)

	tti := rows[0]
	assert.Equal(t, "app-1", tti.AppID)
	// iOS sends uppercase UUIDs; update ids are normalized to lowercase.
	assert.Equal(t, "4127C568-AF7F-4D2B-9E0A-1C6E2B7D9F31", tti.EASClientID)
	assert.Equal(t, "9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10", tti.UpdateID)
	assert.Equal(t, "production", tti.Channel)
	assert.Equal(t, "1.4.0", tti.RuntimeVersion)
	assert.Equal(t, "ios", tti.Platform)
	assert.Equal(t, "iOS", tti.OSName)
	assert.Equal(t, "26.2", tti.OSVersion)
	// device.model.identifier wins over device.model.name.
	assert.Equal(t, "iPhone17,3", tti.DeviceModel)
	assert.Equal(t, "1.4.0", tti.AppVersion)
	assert.Equal(t, "421", tti.AppBuildNumber)
	assert.Equal(t, "7F2C1D3E-8B4A-4C5D-9E6F-0A1B2C3D4E5F", tti.EASBuildID)
	assert.Equal(t, "production", tti.Environment)
	assert.Equal(t, "57.0.7", tti.SDKVersion)
	assert.Equal(t, "expo.app_startup.tti", tti.MetricName)
	assert.InDelta(t, 1.842, tti.Value, 0.0001)
	// session.id goes through UUID normalization too (lowercased).
	assert.Equal(t, "09ced20b-7e4a-4c3b-a2d1-5f6e7a8b9c0d", tti.SessionID)
	assert.Contains(t, tti.CustomParams, "expo.frameRate.slowFrames")
	// In-range wire timestamp preserved, not clamped.
	assert.Equal(t, time.Unix(0, 1767960489000000000).UTC(), tti.Timestamp)
	assert.NotEqual(t, uuid.UUID{}, tti.ContentKey)

	nav := rows[1]
	assert.Equal(t, "expo.navigation.cold_ttr", nav.MetricName)
	assert.Equal(t, "/orders/9B3B89B6-5A0D-4A57-B1F5-6E1D5B7C2A10/items/42", nav.RouteName)
}

func TestFlattenMetricsAndroid(t *testing.T) {
	batch, err := DecodeMetrics(bytes.NewReader(loadFixture(t, "android_metrics.json")))
	require.NoError(t, err)
	rows := FlattenMetrics("app-1", batch, flattenNow)
	require.Len(t, rows, 2)

	warm := rows[0]
	// No os.name on this payload: platform falls back to the SDK language.
	assert.Equal(t, "android", warm.Platform)
	// No update id at all: embedded-bundle sentinel, never empty.
	assert.Equal(t, ZeroUpdateID, warm.UpdateID)
	assert.Equal(t, "", warm.Channel)
	// The global attribute ("panier", wire intValue) survives in the
	// attributes JSON with its type intact.
	assert.Contains(t, warm.Attributes, `"panier":3`)
	// session.id is envelope, never duplicated into attributes.
	assert.NotContains(t, warm.Attributes, "session.id")
}

func TestFlattenLogsIOS(t *testing.T) {
	batch, err := DecodeLogs(bytes.NewReader(loadFixture(t, "ios_logs.json")))
	require.NoError(t, err)
	rows := FlattenLogs("app-1", batch, flattenNow)
	require.Len(t, rows, 3)

	exc := rows[0]
	assert.Equal(t, "exception", exc.EventName)
	assert.EqualValues(t, 21, exc.SeverityNumber)
	assert.Equal(t, "FATAL", exc.SeverityText)
	assert.True(t, exc.IsFatal)
	assert.Equal(t, "TypeError: undefined is not a function", exc.Body)
	assert.Equal(t, "9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10", exc.UpdateID)
	// Exception details live in the attributes JSON; envelope keys do not.
	assert.Contains(t, exc.Attributes, "exception.type")
	assert.NotContains(t, exc.Attributes, "event.name")
	assert.NotContains(t, exc.Attributes, "expo.error.is_fatal")

	warn := rows[1]
	assert.Equal(t, "expo.memory.warning", warn.EventName)
	assert.EqualValues(t, 13, warn.SeverityNumber)
	assert.Equal(t, "", warn.Body)
}

func TestFlattenLogsSkipsIdentityOps(t *testing.T) {
	body := []byte(`{"resourceLogs":[{"resource":{"attributes":[
		{"key":"expo.eas_client.id","value":{"stringValue":"3f9b2c81-4a5d-4e6f-8a9b-0c1d2e3f4a5b"}}]},
		"scopeLogs":[{"logRecords":[
			{"attributes":[{"key":"event.name","value":{"stringValue":"$set"}},{"key":"userId","value":{"stringValue":"u-1"}}]},
			{"attributes":[{"key":"event.name","value":{"stringValue":"checkout"}}]}
		]}]}]}`)
	batch, err := DecodeLogs(bytes.NewReader(body))
	require.NoError(t, err)
	rows := FlattenLogs("app-1", batch, flattenNow)
	// $set is identity's, only the telemetry record lands.
	require.Len(t, rows, 1)
	assert.Equal(t, "checkout", rows[0].EventName)
}

func TestFlattenDropsForgedClientID(t *testing.T) {
	body := []byte(`{"resourceMetrics":[{"resource":{"attributes":[
		{"key":"expo.eas_client.id","value":{"stringValue":"not-a-uuid"}}]},
		"scopeMetrics":[{"metrics":[{"name":"expo.app_startup.tti","gauge":{"dataPoints":[{"timeUnixNano":1,"asDouble":1}]}}]}]}]}`)
	batch, err := DecodeMetrics(bytes.NewReader(body))
	require.NoError(t, err)
	assert.Empty(t, FlattenMetrics("app-1", batch, flattenNow))
}

func TestFlattenMetricsPointUpdateIDOverride(t *testing.T) {
	body := []byte(`{"resourceMetrics":[{"resource":{"attributes":[
		{"key":"expo.eas_client.id","value":{"stringValue":"3f9b2c81-4a5d-4e6f-8a9b-0c1d2e3f4a5b"}},
		{"key":"expo.app.updates.id","value":{"stringValue":"9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10"}}]},
		"scopeMetrics":[{"metrics":[{"name":"expo.updates.download_time","gauge":{"dataPoints":[
			{"timeUnixNano":1767960489000000000,"asDouble":2.5,"attributes":[
				{"key":"expo.update_id","value":{"stringValue":"AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"}}]}]}}]}]}]}`)
	batch, err := DecodeMetrics(bytes.NewReader(body))
	require.NoError(t, err)
	rows := FlattenMetrics("app-1", batch, flattenNow)
	require.Len(t, rows, 1)
	// download_time is about the update just downloaded, not the running one.
	assert.Equal(t, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", rows[0].UpdateID)
}

func TestClampTimestamp(t *testing.T) {
	now := flattenNow
	// Zero (unparseable client date) and bogus clocks map to ingestion time.
	assert.Equal(t, now, clampTimestamp(0, now))
	assert.Equal(t, now, clampTimestamp(uint64(now.Add(48*time.Hour).UnixNano()), now))
	assert.Equal(t, now, clampTimestamp(uint64(now.Add(-500*24*time.Hour).UnixNano()), now))
	// In range passes through.
	inRange := now.Add(-time.Hour)
	assert.Equal(t, inRange, clampTimestamp(uint64(inRange.UnixNano()), now))
}

func TestFlattenDeterministicHashes(t *testing.T) {
	fixture := loadFixture(t, "ios_logs.json")
	batch1, err := DecodeLogs(bytes.NewReader(fixture))
	require.NoError(t, err)
	batch2, err := DecodeLogs(bytes.NewReader(fixture))
	require.NoError(t, err)
	rows1 := FlattenLogs("app-1", batch1, flattenNow)
	rows2 := FlattenLogs("app-1", batch2, flattenNow.Add(time.Hour))
	require.Equal(t, len(rows1), len(rows2))
	// A retried batch hashes identically whenever it re-arrives: the hash
	// reads the raw wire nano, never the (time-dependent) clamped value.
	for i := range rows1 {
		assert.Equal(t, rows1[i].ContentKey, rows2[i].ContentKey, "row %d", i)
	}
}

func TestDecodeToleratesUnknownFields(t *testing.T) {
	batch, err := DecodeMetrics(bytes.NewReader(loadFixture(t, "unknown_fields.json")))
	require.NoError(t, err)
	assert.NotEmpty(t, batch.Resources)
}

// logsWithoutSession is the shape that makes the hash's device component load
// bearing: no session.id anywhere, which normalizeSessionID folds onto the zero
// uuid for every record of every device.
func logsWithoutSession(clientID, body string) []byte {
	return []byte(`{"resourceLogs":[{"resource":{"attributes":[
		{"key":"expo.eas_client.id","value":{"stringValue":"` + clientID + `"}}]},
		"scopeLogs":[{"logRecords":[{"timeUnixNano":1767960489000000000,"severityNumber":9,
		"severityText":"INFO","body":{"stringValue":"` + body + `"},
		"attributes":[{"key":"event.name","value":{"stringValue":"exception"}}]}]}]}]}`)
}

func hashOfSingleLog(t *testing.T, body []byte) uuid.UUID {
	t.Helper()
	batch, err := DecodeLogs(bytes.NewReader(body))
	require.NoError(t, err)
	rows := FlattenLogs("app-1", batch, time.Now().UTC())
	require.Len(t, rows, 1)
	return rows[0].ContentKey
}

// Two phones logging the same line at the same instant are two records, and the
// dedup key has to say so. Without the device in the hash they shared one, since
// a missing session collapses onto a single value for the whole fleet.
func TestContentKeySeparatesDevicesWithoutASession(t *testing.T) {
	first := hashOfSingleLog(t, logsWithoutSession("8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d", "fetch failed"))
	second := hashOfSingleLog(t, logsWithoutSession("7a6b5c4d-3e2f-1a0b-9c8d-7e6f5a4b3c2d", "fetch failed"))
	require.NotEqual(t, first, second, "two devices must not share one dedup identity")

	// And the same device still hashes to itself, which is what makes a
	// re-sent batch collapse rather than double.
	repeated := hashOfSingleLog(t, logsWithoutSession("8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d", "fetch failed"))
	require.Equal(t, first, repeated)
}

// is_fatal and severity_text are stored columns pulled out of the attributes, so
// they are not covered by the attributes JSON the hash already carries. Two rows
// that read differently on screen must not share an identity.
func TestContentKeySeparatesFatalFromNonFatal(t *testing.T) {
	const client = "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d"
	plain := logsWithoutSession(client, "boom")
	fatal := []byte(strings.Replace(string(plain),
		`{"key":"event.name","value":{"stringValue":"exception"}}`,
		`{"key":"event.name","value":{"stringValue":"exception"}},{"key":"is_fatal","value":{"boolValue":true}}`, 1))
	require.NotEqual(t, hashOfSingleLog(t, plain), hashOfSingleLog(t, fatal))

	warned := []byte(strings.Replace(string(plain), `"severityText":"INFO"`, `"severityText":"WARN"`, 1))
	require.NotEqual(t, hashOfSingleLog(t, plain), hashOfSingleLog(t, warned),
		"severity_text is stored, so it is part of what a row is")
}

// Every content field is bounded, at the same limits the client already
// enforces before it stores anything. None of these columns has a length of
// its own: ClickHouse String is unbounded, and the per-batch ceiling counts
// records rather than bytes, so one forged record could otherwise carry
// megabytes.
func TestContentFieldsAreBounded(t *testing.T) {
	huge := strings.Repeat("A", 200_000)
	client := "4127c568-af7f-4d2b-9e0a-1c6e2b7d9f31"

	logs := FlattenLogs("app-1", LogBatch{Resources: []ResourceLogs{{
		Attributes: map[string]any{EASClientIDKey: client},
		Records: []LogRecord{{
			TimeUnixNano: uint64(flattenNow.UnixNano()),
			Body:         huge,
			SeverityText: huge,
			Attributes:   map[string]any{EventNameKey: huge, "user.note": huge},
		}},
	}}}, flattenNow)
	require.Len(t, logs, 1)
	assert.Len(t, []rune(logs[0].Body), maxBodyRunes)
	assert.Len(t, []rune(logs[0].EventName), maxEventNameRunes)
	assert.Len(t, []rune(logs[0].SeverityText), maxSeverityTextRunes)
	assert.LessOrEqual(t, len(logs[0].Attributes), maxAttributesBytes+1024,
		"the attribute blob must stay inside its ceiling")

	metrics := FlattenMetrics("app-1", MetricBatch{Resources: []ResourceMetrics{{
		Attributes: map[string]any{EASClientIDKey: client},
		Points: []MetricPoint{{
			MetricName:   huge,
			TimeUnixNano: uint64(flattenNow.UnixNano()),
			Value:        1,
			Attributes:   map[string]any{routeNameKey: huge, customParamsKey: huge},
		}},
	}}}, flattenNow)
	require.Len(t, metrics, 1)
	assert.Len(t, []rune(metrics[0].MetricName), maxMetricNameRunes)
	assert.Len(t, []rune(metrics[0].RouteName), maxRouteNameRunes)
	assert.Len(t, []rune(metrics[0].CustomParams), maxCustomParamsRunes)
}

// The ceiling has to hold for what an attribute can actually BE, not only for
// the strings it is easiest to test with. OTLP carries arrayValue and
// kvlistValue, which arrive here as []any and map[string]any and nest as deep
// as the body allows. Charging them a flat 64 bytes and then serializing them
// in full let a single attribute walk the whole batch past this ceiling and
// into a stored row, which is precisely the amplification it exists to stop.
func TestNestedAttributesAreChargedWhatTheyCost(t *testing.T) {
	client := "4127c568-af7f-4d2b-9e0a-1c6e2b7d9f31"
	// One array and one object, each far past the ceiling on their own, and
	// each worth 64 bytes under the old accounting.
	fat := make([]any, 40_000)
	for i := range fat {
		fat[i] = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	}
	nested := map[string]any{}
	for i := 0; i < 20_000; i++ {
		nested[fmt.Sprintf("k%05d", i)] = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	}

	logs := FlattenLogs("app-1", LogBatch{Resources: []ResourceLogs{{
		Attributes: map[string]any{EASClientIDKey: client},
		Records: []LogRecord{{
			TimeUnixNano: uint64(flattenNow.UnixNano()),
			Body:         "hello",
			Attributes:   map[string]any{"a.list": fat, "b.object": nested},
		}},
	}}}, flattenNow)
	require.Len(t, logs, 1)
	assert.LessOrEqual(t, len(logs[0].Attributes), maxAttributesBytes+1024,
		"a nested attribute must be charged what it serializes to, not a flat guess")

	// And a nested value that FITS is still stored, unflattened: the fix is a
	// measurement, not a ban on structure.
	small := map[string]any{"plan": "pro", "seats": float64(3)}
	kept := FlattenLogs("app-1", LogBatch{Resources: []ResourceLogs{{
		Attributes: map[string]any{EASClientIDKey: client},
		Records: []LogRecord{{
			TimeUnixNano: uint64(flattenNow.UnixNano()),
			Body:         "hello",
			Attributes:   map[string]any{"context": small},
		}},
	}}}, flattenNow)
	require.Len(t, kept, 1)
	assert.JSONEq(t, `{"context":{"plan":"pro","seats":3}}`, kept[0].Attributes)
}

// The attribute map is bounded in COUNT as well as in bytes, and the survivors
// are the alphabetically first, which is the rule the client applies too: both
// ends then keep the same attributes rather than each keeping its own set.
func TestAttributeCountIsBoundedAlphabetically(t *testing.T) {
	attrs := map[string]any{}
	for i := 0; i < maxAttributesPerRecord+50; i++ {
		attrs[fmt.Sprintf("k%04d", i)] = "v"
	}
	out := marshalAttributes(attrs, map[string]bool{})

	var kept map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &kept))
	assert.Len(t, kept, maxAttributesPerRecord)
	assert.Contains(t, kept, "k0000", "the alphabetically first must survive")
	assert.NotContains(t, kept, fmt.Sprintf("k%04d", maxAttributesPerRecord+49),
		"the alphabetically last must be the one dropped")
}

// An honest client is never touched: it truncated itself first, so the server
// bound must be a no-op on anything inside the contract.
func TestBoundsLeaveAnHonestRecordAlone(t *testing.T) {
	body := strings.Repeat("b", maxBodyRunes)
	client := "4127c568-af7f-4d2b-9e0a-1c6e2b7d9f31"
	logs := FlattenLogs("app-1", LogBatch{Resources: []ResourceLogs{{
		Attributes: map[string]any{EASClientIDKey: client},
		Records: []LogRecord{{
			TimeUnixNano: uint64(flattenNow.UnixNano()),
			Body:         body,
			Attributes:   map[string]any{EventNameKey: "checkout_completed"},
		}},
	}}}, flattenNow)
	require.Len(t, logs, 1)
	assert.Equal(t, body, logs[0].Body, "a body at the contract limit passes whole")
	assert.Equal(t, "checkout_completed", logs[0].EventName)
}
