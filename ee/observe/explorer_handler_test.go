// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"expo-open-ota/ee/identity"
)

type recordingExplorer struct {
	overviewQuery  ExplorerQuery
	logsQuery      LogsQuery
	breakdownQuery BreakdownQuery
	checkInQuery   CheckInQuery
}

func (r *recordingExplorer) ReadCheckIns(_ context.Context, _ string, query CheckInQuery) (CheckInFeed, error) {
	r.checkInQuery = query
	return CheckInFeed{Cities: []ObserveLocation{}, Cursor: query.Since}, nil
}

func (r *recordingExplorer) ReadOverview(_ context.Context, _ string, query ExplorerQuery) (Overview, error) {
	r.overviewQuery = query
	return Overview{Available: true, Metrics: []MetricSeries{}, Locations: []ObserveLocation{}}, nil
}

func (r *recordingExplorer) ReadEvents(_ context.Context, _ string, query ExplorerQuery) (Events, error) {
	r.overviewQuery = query
	return Events{Available: true, Events: []ObserveEventSeries{}}, nil
}

func (r *recordingExplorer) ReadLogs(_ context.Context, _ string, query LogsQuery) (LogsPage, error) {
	r.logsQuery = query
	return LogsPage{Available: true, Logs: []ObserveLog{}}, nil
}

func (r *recordingExplorer) ReadBreakdown(_ context.Context, _ string, query BreakdownQuery) (Breakdown, error) {
	r.breakdownQuery = query
	return Breakdown{Available: true, Segments: []BreakdownSegment{}}, nil
}

type staticSchema struct {
	schema identity.Schema
}

func (s staticSchema) GetSchema(context.Context, string) (identity.Schema, error) {
	return s.schema, nil
}

func serveExplorer(handler *ExplorerHandler, path string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	router.HandleFunc("/api/apps/{APP_ID}/observe/overview", handler.GetOverviewHandler)
	router.HandleFunc("/api/apps/{APP_ID}/observe/events", handler.GetEventsHandler)
	router.HandleFunc("/api/apps/{APP_ID}/observe/logs", handler.GetLogsHandler)
	router.HandleFunc("/api/apps/{APP_ID}/observe/breakdown", handler.GetBreakdownHandler)
	router.HandleFunc("/api/apps/{APP_ID}/observe/check-ins", handler.GetCheckInsHandler)
	request := httptest.NewRequest(http.MethodGet, "/api/apps/"+uuid.NewString()+path, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestExplorerHandlerReportsUnavailableWithoutReader(t *testing.T) {
	recorder := serveExplorer(NewExplorerHandler(nil, nil), "/observe/overview")
	require.Equal(t, http.StatusOK, recorder.Code)

	var response Overview
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.False(t, response.Available)
	require.Empty(t, response.Locations)
}

func TestExplorerHandlerParsesTypedIdentityFilter(t *testing.T) {
	reader := &recordingExplorer{}
	handler := NewExplorerHandler(reader, staticSchema{schema: identity.Schema{
		"planLevel": {Key: "planLevel", Type: identity.ValueTypeNumber, MaxLength: 256},
	}})
	recorder := serveExplorer(
		handler,
		"/observe/overview?attr=planLevel:42&platform=ios",
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []string{"ios"}, reader.overviewQuery.Platform)
	require.Len(t, reader.overviewQuery.MetadataFilter, 1)
	require.JSONEq(t, `{"planLevel":42}`, string(reader.overviewQuery.MetadataFilter[0]))
}

func TestExplorerHandlerParsesSeveralIdentityValues(t *testing.T) {
	reader := &recordingExplorer{}
	handler := NewExplorerHandler(reader, staticSchema{schema: identity.Schema{
		"plan": {Key: "plan", Type: identity.ValueTypeString, MaxLength: 256},
	}})
	recorder := serveExplorer(
		handler,
		"/observe/overview?attr=plan:pro&attr=plan:enterprise",
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, reader.overviewQuery.MetadataFilter, 2)
	require.JSONEq(t, `{"plan":"pro"}`, string(reader.overviewQuery.MetadataFilter[0]))
	require.JSONEq(t, `{"plan":"enterprise"}`, string(reader.overviewQuery.MetadataFilter[1]))

	typed := NewExplorerHandler(reader, staticSchema{schema: identity.Schema{
		"planLevel": {Key: "planLevel", Type: identity.ValueTypeNumber, MaxLength: 256},
	}})
	bad := serveExplorer(typed, "/observe/overview?attr=planLevel:42&attr=planLevel:perhaps")
	require.Equal(t, http.StatusBadRequest, bad.Code)
}

func TestExplorerHandlerParsesSeveralIdentityAttributes(t *testing.T) {
	reader := &recordingExplorer{}
	handler := NewExplorerHandler(reader, staticSchema{schema: identity.Schema{
		"plan":   {Key: "plan", Type: identity.ValueTypeString, MaxLength: 256},
		"tenant": {Key: "tenant", Type: identity.ValueTypeString, MaxLength: 256},
	}})
	recorder := serveExplorer(
		handler,
		"/observe/overview?attr=plan:pro&attr=plan:enterprise&attr=tenant:globex",
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, reader.overviewQuery.MetadataFilter, 2)
	require.JSONEq(t, `{"plan":"pro","tenant":"globex"}`, string(reader.overviewQuery.MetadataFilter[0]))
	require.JSONEq(
		t,
		`{"plan":"enterprise","tenant":"globex"}`,
		string(reader.overviewQuery.MetadataFilter[1]),
	)

	require.Equal(t, http.StatusBadRequest, serveExplorer(handler, "/observe/overview?attr=plan").Code)
	require.Equal(
		t,
		http.StatusBadRequest,
		serveExplorer(handler, "/observe/overview?attr=nothing:x").Code,
	)
}

func TestExplorerHandlerParsesTelemetryDimensions(t *testing.T) {
	reader := &recordingExplorer{}
	updateID := uuid.NewString()
	groupID := uuid.NewString()
	clientID := uuid.NewString()
	recorder := serveExplorer(
		NewExplorerHandler(reader, nil),
		"/observe/events?updateId="+updateID+
			"&updateGroupId="+groupID+
			"&easClientId="+clientID+
			"&branch=production&runtimeVersion=3.0.0&channel=stable",
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []string{updateID}, reader.overviewQuery.UpdateIDs)
	require.Equal(t, []string{groupID}, reader.overviewQuery.UpdateGroupIDs)
	require.Equal(t, []string{clientID}, reader.overviewQuery.EASClientIDs)
	require.Equal(t, []string{"production"}, reader.overviewQuery.Branches)
	require.Equal(t, []string{"3.0.0"}, reader.overviewQuery.RuntimeVersions)
	require.Equal(t, []string{"stable"}, reader.overviewQuery.Channels)
}

func TestExplorerLogsRejectsInvalidCursor(t *testing.T) {
	recorder := serveExplorer(NewExplorerHandler(&recordingExplorer{}, nil), "/observe/logs?cursor=nope")
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestLogCursorRoundTrip(t *testing.T) {
	cursor := LogCursor{Timestamp: time.Date(2026, 7, 24, 10, 0, 0, 123, time.UTC), EventKey: "5a2b1c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"}
	decoded, err := DecodeLogCursor(EncodeLogCursor(cursor))
	require.NoError(t, err)
	require.Equal(t, cursor, *decoded)
}

func TestLogCursorRefusesAnythingButAnExactKey(t *testing.T) {
	timestamp := time.Date(2026, 7, 24, 10, 0, 0, 123, time.UTC).Format(time.RFC3339Nano)
	for _, key := range []string{"42junk", "0x10", " 42", "42 99", "", "-1", "4.2"} {
		encoded := base64.RawURLEncoding.EncodeToString([]byte(timestamp + "|" + key))
		decoded, err := DecodeLogCursor(encoded)
		require.Error(t, err, "key %q", key)
		require.Nil(t, decoded, "key %q", key)
	}

	for _, key := range []string{"5a2b1c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", "42"} {
		valid := base64.RawURLEncoding.EncodeToString([]byte(timestamp + "|" + key))
		decoded, err := DecodeLogCursor(valid)
		require.NoError(t, err, "key %q", key)
		require.Equal(t, key, decoded.EventKey)
	}
}

func TestExplorerHandlerParsesHardwareDimensions(t *testing.T) {
	reader := &recordingExplorer{}
	recorder := serveExplorer(
		NewExplorerHandler(reader, nil),
		"/observe/events?osName=iOS&osVersion=26.1&deviceModel=iPhone18%2C2",
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []string{"iOS"}, reader.overviewQuery.OSNames)
	require.Equal(t, []string{"26.1"}, reader.overviewQuery.OSVersions)
	require.Equal(t, []string{"iPhone18,2"}, reader.overviewQuery.DeviceModels)
}

func TestExplorerCheckInsClampsStaleCursor(t *testing.T) {
	reader := &recordingExplorer{}
	stale := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	recorder := serveExplorer(NewExplorerHandler(reader, nil), "/observe/check-ins?since="+stale)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.WithinDuration(
		t,
		time.Now().UTC().Add(-maxCheckInLookback),
		reader.checkInQuery.Since,
		2*time.Second,
	)
}

func TestExplorerCheckInsClampsFutureCursor(t *testing.T) {
	reader := &recordingExplorer{}
	ahead := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	recorder := serveExplorer(NewExplorerHandler(reader, nil), "/observe/check-ins?since="+ahead)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.WithinDuration(t, time.Now().UTC(), reader.checkInQuery.Since, 2*time.Second)
}

func TestExplorerCheckInsRejectsUnparsableCursor(t *testing.T) {
	recorder := serveExplorer(NewExplorerHandler(&recordingExplorer{}, nil), "/observe/check-ins?since=nope")
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestExplorerCheckInsWithoutReaderIsEmpty(t *testing.T) {
	recorder := serveExplorer(NewExplorerHandler(nil, nil), "/observe/check-ins")
	require.Equal(t, http.StatusOK, recorder.Code)

	var feed CheckInFeed
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &feed))
	require.Empty(t, feed.Cities)
	require.False(t, feed.Cursor.IsZero())
}

func TestExplorerBreakdownRejectsUnknownDimension(t *testing.T) {
	recorder := serveExplorer(
		NewExplorerHandler(&recordingExplorer{}, nil),
		"/observe/breakdown?metric=cold-launch&dimension=os_version%3B+DROP",
	)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestExplorerBreakdownRequiresMetric(t *testing.T) {
	recorder := serveExplorer(
		NewExplorerHandler(&recordingExplorer{}, nil),
		"/observe/breakdown?dimension=deviceModel",
	)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestExplorerBreakdownTakesOneDimension(t *testing.T) {
	reader := &recordingExplorer{}
	recorder := serveExplorer(
		NewExplorerHandler(reader, nil),
		"/observe/breakdown?metric=cold-launch&dimension=deviceModel,country&points=1",
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "deviceModel", reader.breakdownQuery.Dimension)
	require.True(t, reader.breakdownQuery.WithPoints)

	rejected := serveExplorer(
		NewExplorerHandler(&recordingExplorer{}, nil),
		"/observe/breakdown?metric=cold-launch&dimension=nope",
	)
	require.Equal(t, http.StatusBadRequest, rejected.Code)

	missing := serveExplorer(
		NewExplorerHandler(&recordingExplorer{}, nil),
		"/observe/breakdown?metric=cold-launch",
	)
	require.Equal(t, http.StatusBadRequest, missing.Code)
}

func TestExplorerBreakdownPassesDimensionThrough(t *testing.T) {
	reader := &recordingExplorer{}
	recorder := serveExplorer(
		NewExplorerHandler(reader, nil),
		"/observe/breakdown?metric=cold-launch&dimension=deviceModel&limit=10",
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "cold-launch", reader.breakdownQuery.Metric)
	require.Equal(t, "deviceModel", reader.breakdownQuery.Dimension)
	require.Equal(t, 10, reader.breakdownQuery.Limit)
}

func TestExplorerBreakdownReportsUnavailableWithoutReader(t *testing.T) {
	recorder := serveExplorer(
		NewExplorerHandler(nil, nil),
		"/observe/breakdown?metric=cold-launch&dimension=osVersion",
	)
	require.Equal(t, http.StatusOK, recorder.Code)

	var response Breakdown
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.False(t, response.Available)
	require.Empty(t, response.Segments)
}

// TestBreakdownDimensionsAreAllMapped verifies every dimension resolves to
// exactly one of a column or an expression.
func TestBreakdownDimensionsAreAllMapped(t *testing.T) {
	for name, dimension := range breakdownDimensions {
		require.True(t, IsBreakdownDimension(name), name)
		require.NotEqual(t, dimension.column == "", dimension.expr == "", name)
	}
	require.False(t, IsBreakdownDimension("value"))
	require.False(t, IsBreakdownDimension(""))
}

func TestConditionDimensionsAreTheExpressionOnes(t *testing.T) {
	require.Equal(
		t,
		[]string{"frozenFrames", "lowPowerMode", "networkBytes", "networkType", "thermalState"},
		ConditionDimensions(),
	)
	for _, name := range ConditionDimensions() {
		require.NotEmpty(t, breakdownDimensions[name].expr, name)
	}
}

// TestBreakdownSurvivesEmptyPercentiles verifies finite() replaces NaN/Inf
// percentiles, which JSON cannot marshal.
func TestBreakdownSurvivesEmptyPercentiles(t *testing.T) {
	require.Equal(t, 0.0, finite(math.NaN()))
	require.Equal(t, 0.0, finite(math.Inf(1)))
	require.Equal(t, 0.0, finite(math.Inf(-1)))
	require.Equal(t, 1.25, finite(1.25))

	_, err := json.Marshal(Breakdown{Overall: BreakdownSegment{P50: math.NaN()}})
	require.Error(t, err, "a NaN percentile must not be serializable, which is why finite exists")
	body, err := json.Marshal(Breakdown{Overall: BreakdownSegment{P50: finite(math.NaN())}})
	require.NoError(t, err)
	require.Contains(t, string(body), `"p50":0`)
}

func TestConditionsWhereBindsItsValues(t *testing.T) {
	where, args := conditionsWhere(map[string][]string{
		"thermalState": {"serious", "critical"},
		"networkType":  {"cellular"},
	})
	require.Equal(t, []any{[]string{"cellular"}, []string{"serious", "critical"}}, args)
	predicate := string(where)
	require.Less(
		t,
		strings.Index(predicate, "expo.network.type"),
		strings.Index(predicate, "expo.device.thermalState"),
	)
	require.Equal(t, 2, strings.Count(predicate, " IN ?"))

	empty, noArgs := conditionsWhere(map[string][]string{"thermalState": {}})
	require.Empty(t, empty)
	require.Empty(t, noArgs)
}

func TestExplorerHandlerParsesMultipleFilterValues(t *testing.T) {
	reader := &recordingExplorer{}
	recorder := serveExplorer(
		NewExplorerHandler(reader, nil),
		"/observe/events?branch=production&branch=staging&platform=ios&platform=android"+
			"&deviceModel=iPhone18%2C2&deviceModel=SM-A546B&osVersion=26.1&osVersion=18.6",
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []string{"production", "staging"}, reader.overviewQuery.Branches)
	require.Equal(t, []string{"ios", "android"}, reader.overviewQuery.Platform)
	require.Equal(t, []string{"26.1", "18.6"}, reader.overviewQuery.OSVersions)
	require.Equal(t, []string{"iPhone18,2", "SM-A546B"}, reader.overviewQuery.DeviceModels)
}

func TestExplorerHandlerRejectsTooManyFilterValues(t *testing.T) {
	values := make([]string, 0, maxFilterValues+1)
	for i := 0; i <= maxFilterValues; i++ {
		values = append(values, fmt.Sprintf("branch=branch-%d", i))
	}
	recorder := serveExplorer(
		NewExplorerHandler(&recordingExplorer{}, nil),
		"/observe/events?"+strings.Join(values, "&"),
	)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

// TestNativeCrashArmHonoursOnlyWhatItKnows verifies the native-crash arm drops
// out rather than answer filters it has no data to satisfy.
func TestNativeCrashArmHonoursOnlyWhatItKnows(t *testing.T) {
	base := LogsQuery{ExplorerQuery: ExplorerQuery{
		From: time.Now().Add(-time.Hour),
		To:   time.Now(),
	}}

	where, args, ok := nativeCrashArm(base, false)
	require.True(t, ok)
	require.Contains(t, where, "h.failure_type = ?")
	require.Contains(t, args, string(identity.FailureTypeUpdate))

	narrowed := base
	narrowed.Branches = []string{"production"}
	narrowed.DeviceModels = []string{"SM-A546B"}
	where, _, ok = nativeCrashArm(narrowed, false)
	require.True(t, ok)
	require.Contains(t, where, "h.branch IN ?")
	require.Contains(t, where, "h.device_model IN ?")

	grouped := base
	grouped.UpdateGroupIDs = []string{"99999999-9999-9999-9999-999999999999"}
	grouped.MemberUpdateIDs = []string{"11111111-1111-1111-1111-111111111111"}
	where, args, ok = nativeCrashArm(grouped, false)
	require.True(t, ok)
	require.Contains(t, where, "h.update_id IN ?")
	require.Contains(t, args, []string{"11111111-1111-1111-1111-111111111111"})

	// An update AND a group is an intersection: two predicates, not one merged list.
	both := grouped
	both.UpdateIDs = []string{"22222222-2222-2222-2222-222222222222"}
	where, args, ok = nativeCrashArm(both, false)
	require.True(t, ok)
	require.Equal(t, 2, strings.Count(string(where), "h.update_id IN ?"))
	require.Contains(t, args, []string{"22222222-2222-2222-2222-222222222222"})
	require.Contains(t, args, []string{"11111111-1111-1111-1111-111111111111"})

	empty := base
	empty.UpdateGroupIDs = []string{"99999999-9999-9999-9999-999999999999"}
	_, _, ok = nativeCrashArm(empty, false)
	require.False(t, ok)

	for _, query := range []LogsQuery{
		withChannels(base, "stable"),
		withEnvironments(base, "production"),
	} {
		_, _, ok := nativeCrashArm(query, false)
		require.False(t, ok)
	}

	for _, severity := range []string{"info", "warn", "debug"} {
		quiet := base
		quiet.Severity = severity
		_, _, ok := nativeCrashArm(quiet, false)
		require.False(t, ok, severity)
	}
	for _, severity := range []string{"", "error", "fatal"} {
		loud := base
		loud.Severity = severity
		_, _, ok := nativeCrashArm(loud, false)
		require.True(t, ok, severity)
	}

	named := base
	named.EventNames = []string{"checkout_started"}
	_, _, ok = nativeCrashArm(named, false)
	require.False(t, ok)
	named.EventNames = []string{"checkout_started", nativeCrashEventName}
	_, _, ok = nativeCrashArm(named, false)
	require.True(t, ok)
}

func withChannels(query LogsQuery, channels ...string) LogsQuery {
	query.Channels = channels
	return query
}

func withEnvironments(query LogsQuery, environments ...string) LogsQuery {
	query.Environments = environments
	return query
}

func TestExplorerHandlerNormalizesPlatformCase(t *testing.T) {
	reader := &recordingExplorer{}
	recorder := serveExplorer(NewExplorerHandler(reader, nil), "/observe/events?platform=IOS&platform=Android")

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []string{"ios", "android"}, reader.overviewQuery.Platform)
}

func TestExplorerHandlerRejectsUnknownPlatform(t *testing.T) {
	recorder := serveExplorer(NewExplorerHandler(&recordingExplorer{}, nil), "/observe/events?platform=windows")
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestExplorerHandlerBoundsTheTimeWindow(t *testing.T) {
	now := time.Now().UTC()
	rfc := func(t time.Time) string { return t.Format(time.RFC3339) }

	for name, query := range map[string]string{
		"from after to":      "?from=" + rfc(now) + "&to=" + rfc(now.Add(-time.Hour)),
		"from equal to":      "?from=" + rfc(now) + "&to=" + rfc(now),
		"beyond the ceiling": "?from=" + rfc(now.Add(-91*24*time.Hour)) + "&to=" + rfc(now),
		"unparsable from":    "?from=yesterday&to=" + rfc(now),
		"unparsable to":      "?from=" + rfc(now.Add(-time.Hour)) + "&to=soon",
	} {
		t.Run(name, func(t *testing.T) {
			recorder := serveExplorer(NewExplorerHandler(&recordingExplorer{}, nil), "/observe/overview"+query)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}

	within := serveExplorer(
		NewExplorerHandler(&recordingExplorer{}, nil),
		"/observe/overview?from="+rfc(now.Add(-89*24*time.Hour))+"&to="+rfc(now),
	)
	require.Equal(t, http.StatusOK, within.Code)
}

func TestExplorerLogsWindowIsTighterThanOverview(t *testing.T) {
	now := time.Now().UTC()
	rfc := func(t time.Time) string { return t.Format(time.RFC3339) }
	window := "?from=" + rfc(now.Add(-60*24*time.Hour)) + "&to=" + rfc(now)

	require.Equal(t, http.StatusOK,
		serveExplorer(NewExplorerHandler(&recordingExplorer{}, nil), "/observe/overview"+window).Code)
	require.Equal(t, http.StatusBadRequest,
		serveExplorer(NewExplorerHandler(&recordingExplorer{}, nil), "/observe/logs"+window).Code)
}

func TestExplorerLogsParsesEventNames(t *testing.T) {
	reader := &recordingExplorer{}
	recorder := serveExplorer(
		NewExplorerHandler(reader, nil),
		"/observe/logs?eventName=checkout_started&eventName=exception",
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []string{"checkout_started", "exception"}, reader.logsQuery.EventNames)
}

func TestExplorerLogsRejectsTooManyEventNames(t *testing.T) {
	path := "/observe/logs?eventName=a"
	for i := 0; i < maxFilterValues; i++ {
		path += "&eventName=" + strconv.Itoa(i)
	}
	recorder := serveExplorer(NewExplorerHandler(&recordingExplorer{}, nil), path)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestExplorerRejectsLimitsOutOfRange(t *testing.T) {
	for _, path := range []string{
		"/observe/logs?limit=0",
		"/observe/logs?limit=-1",
		"/observe/logs?limit=501",
		"/observe/logs?limit=abc",
		"/observe/breakdown?metric=cold_start&dimension=osVersion&limit=0",
		"/observe/breakdown?metric=cold_start&dimension=osVersion&limit=51",
		"/observe/breakdown?metric=cold_start&dimension=osVersion&limit=abc",
	} {
		recorder := serveExplorer(NewExplorerHandler(&recordingExplorer{}, nil), path)
		require.Equal(t, http.StatusBadRequest, recorder.Code, path)
	}
}

func TestExplorerBreakdownStubCarriesABaseline(t *testing.T) {
	recorder := serveExplorer(NewExplorerHandler(nil, nil), "/observe/breakdown?metric=cold_start&dimension=osVersion")
	require.Equal(t, http.StatusOK, recorder.Code)

	var response map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	overall, ok := response["overall"].(map[string]any)
	require.True(t, ok, "overall must be an object")
	require.Equal(t, "", overall["value"])
	require.EqualValues(t, 0, overall["devices"])
	require.NotNil(t, response["segments"], "segments must be a list, never null")
}
