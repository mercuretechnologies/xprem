// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

type recordingHealthHistoryReader struct {
	appID     string
	updateIDs []string
	from      time.Time
	to        time.Time
	points    map[string][]HealthHistoryPoint
	dimension string
	segments  map[string][]HealthSegmentPoint
}

func (r *recordingHealthHistoryReader) ReadBySegment(
	_ context.Context,
	appID string,
	updateIDs []string,
	dimension string,
	from, to time.Time,
) (map[string][]HealthSegmentPoint, error) {
	r.appID = appID
	r.updateIDs = updateIDs
	r.dimension = dimension
	r.from = from
	r.to = to
	return r.segments, nil
}

func (r *recordingHealthHistoryReader) Read(
	_ context.Context,
	appID string,
	updateIDs []string,
	from, to time.Time,
) (map[string][]HealthHistoryPoint, error) {
	r.appID = appID
	r.updateIDs = updateIDs
	r.from = from
	r.to = to
	return r.points, nil
}

func serveHealthHistory(handler *HealthHistoryHandler, appID, query string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	router.HandleFunc(
		"/api/apps/{APP_ID}/observe/update-health/history",
		handler.GetUpdateHealthHistoryHandler,
	).Methods(http.MethodGet)
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/apps/"+appID+"/observe/update-health/history?"+query,
		nil,
	)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestHealthHistoryHandlerReturnsUnavailableWithoutClickHouse(t *testing.T) {
	recorder := serveHealthHistory(
		NewHealthHistoryHandler(nil),
		uuid.NewString(),
		"ids="+uuid.NewString(),
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"available":false,"updates":{}}`, recorder.Body.String())
}

func TestHealthHistoryHandlerReturnsUnavailableWithTypedNilHistory(t *testing.T) {
	var history *HealthHistory
	recorder := serveHealthHistory(
		NewHealthHistoryHandler(history),
		uuid.NewString(),
		"ids="+uuid.NewString(),
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"available":false,"updates":{}}`, recorder.Body.String())
}

func TestHealthHistoryHandlerReadsRequestedWindow(t *testing.T) {
	appID := uuid.NewString()
	updateA, updateB := uuid.NewString(), uuid.NewString()
	from := "2026-07-23T10:00:00Z"
	to := "2026-07-24T10:00:00Z"
	reader := &recordingHealthHistoryReader{
		points: map[string][]HealthHistoryPoint{
			updateA: {{Timestamp: time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC), Role: "candidate"}},
			updateB: {},
		},
	}

	recorder := serveHealthHistory(
		NewHealthHistoryHandler(reader),
		appID,
		"ids="+updateA+","+updateB+","+updateA+"&from="+from+"&to="+to,
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, appID, reader.appID)
	require.Equal(t, []string{updateA, updateB}, reader.updateIDs)
	require.Equal(t, from, reader.from.Format(time.RFC3339))
	require.Equal(t, to, reader.to.Format(time.RFC3339))
	var response struct {
		Available bool                            `json:"available"`
		Updates   map[string][]HealthHistoryPoint `json:"updates"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Available)
	require.Len(t, response.Updates[updateA], 1)
	require.Empty(t, response.Updates[updateB])
}

func TestHealthHistoryHandlerRejectsInvalidInput(t *testing.T) {
	appID := uuid.NewString()
	updateID := uuid.NewString()
	tests := []string{
		"ids=not-a-uuid",
		"ids=" + updateID + "&from=nope",
		"ids=" + updateID + "&from=2026-07-24T11:00:00Z&to=2026-07-24T10:00:00Z",
	}
	for _, query := range tests {
		t.Run(query, func(t *testing.T) {
			recorder := serveHealthHistory(NewHealthHistoryHandler(nil), appID, query)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

// A split changes the shape of the answer: keys become segment values, and an
// unknown dimension is refused rather than silently ignored.
func TestHealthHistorySplitsBySegment(t *testing.T) {
	reader := &recordingHealthHistoryReader{
		segments: map[string][]HealthSegmentPoint{
			"SM-A546B": {{DevicesOnUpdate: 90, FaultyDevices: 7, SuccessfulDevices: 83}},
		},
	}
	updateID := uuid.NewString()
	recorder := serveHealthHistory(
		NewHealthHistoryHandler(reader),
		uuid.NewString(),
		"ids="+updateID+"&dimension=deviceModel",
	)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "deviceModel", reader.dimension)

	var response struct {
		Available bool                            `json:"available"`
		Dimension string                          `json:"dimension"`
		Segments  map[string][]HealthSegmentPoint `json:"segments"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Available)
	require.Equal(t, "deviceModel", response.Dimension)
	require.Len(t, response.Segments["SM-A546B"], 1)

	rejected := serveHealthHistory(
		NewHealthHistoryHandler(&recordingHealthHistoryReader{}),
		uuid.NewString(),
		"ids="+updateID+"&dimension=easClientId",
	)
	require.Equal(t, http.StatusBadRequest, rejected.Code)
}

// A split rebuilds a device-by-bucket grid from raw events, so an unbounded
// window is a full scan of the telemetry table joined against every health
// event the deployment has kept.
func TestHealthHistoryHandlerBoundsTheWindow(t *testing.T) {
	updateID := uuid.NewString()
	now := time.Now().UTC()
	rfc := func(t time.Time) string { return t.Format(time.RFC3339) }

	beyond := serveHealthHistory(
		NewHealthHistoryHandler(&recordingHealthHistoryReader{}),
		uuid.NewString(),
		"ids="+updateID+"&from="+rfc(now.Add(-91*24*time.Hour))+"&to="+rfc(now),
	)
	require.Equal(t, http.StatusBadRequest, beyond.Code)

	within := serveHealthHistory(
		NewHealthHistoryHandler(&recordingHealthHistoryReader{}),
		uuid.NewString(),
		"ids="+updateID+"&from="+rfc(now.Add(-89*24*time.Hour))+"&to="+rfc(now),
	)
	require.Equal(t, http.StatusOK, within.Code)
}

// Unavailable answers in the shape that was asked for: a caller that requested
// a split reads `segments`, and a missing key is a different failure from an
// empty one.
func TestHealthHistoryUnavailableKeepsTheRequestedShape(t *testing.T) {
	recorder := serveHealthHistory(
		NewHealthHistoryHandler(nil),
		uuid.NewString(),
		"ids="+uuid.NewString()+"&dimension=deviceModel",
	)
	require.Equal(t, http.StatusOK, recorder.Code)

	var response map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, false, response["available"])
	require.Equal(t, "deviceModel", response["dimension"])
	require.NotNil(t, response["segments"], "a split asked for segments, not updates")
}

// The chart can only tell eight colours apart, and the segments kept are the
// ones that carry devices, ranked on their peak rather than on where they
// happen to sit at the end of the window.
func TestTrimSegmentsKeepsTheBusiest(t *testing.T) {
	segments := map[string][]HealthSegmentPoint{
		"quiet":        {{DevicesOnUpdate: 1}, {DevicesOnUpdate: 2}},
		"busiest":      {{DevicesOnUpdate: 900}, {DevicesOnUpdate: 3}},
		"steady":       {{DevicesOnUpdate: 40}, {DevicesOnUpdate: 40}},
		"quieter":      {{DevicesOnUpdate: 0}},
		unknownSegment: {{DevicesOnUpdate: 300}},
	}
	trimmed := TrimSegments(segments, 2)
	require.Len(t, trimmed, 2)
	require.Contains(t, trimmed, "busiest")
	require.Contains(t, trimmed, unknownSegment)

	// Nothing to trim leaves the map alone, identity included.
	small := map[string][]HealthSegmentPoint{"only": {{DevicesOnUpdate: 5}}}
	require.Equal(t, small, TrimSegments(small, 8))

	// Ties break on the name, not on map order: this is the only cut now that
	// the query no longer applies a LIMIT, and a series that comes and goes
	// between two refreshes of an untouched chart reads as data moving.
	tied := map[string][]HealthSegmentPoint{
		"18.0": {{DevicesOnUpdate: 10}},
		"17.4": {{DevicesOnUpdate: 10}},
		"17.1": {{DevicesOnUpdate: 10}},
	}
	for range 20 {
		kept := TrimSegments(tied, 2)
		require.Len(t, kept, 2)
		require.Contains(t, kept, "17.1")
		require.Contains(t, kept, "17.4")
	}
}
