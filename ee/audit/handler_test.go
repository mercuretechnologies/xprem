// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// The admin gate is not exercised here on purpose: it wraps the route in
// internal/router (adminOnly), like every sibling admin endpoint; these tests
// cover the handler's own contract.

package audit

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"xprem/internal/auditlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type listAuditEventsWire struct {
	Events     []map[string]any `json:"events"`
	NextCursor *int64           `json:"nextCursor"`
	Count      int64            `json:"count"`
}

func performList(t *testing.T, service *AuditService, query string) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewAuditHandler(service)
	request := httptest.NewRequest(http.MethodGet, "/api/audit/events?"+query, nil)
	recorder := httptest.NewRecorder()
	handler.ListAuditLogsHandler(recorder, request)
	return recorder
}

func decodeList(t *testing.T, recorder *httptest.ResponseRecorder) listAuditEventsWire {
	t.Helper()
	var wire listAuditEventsWire
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &wire))
	return wire
}

func TestListHandlerPassesEveryFilter(t *testing.T) {
	repo := &fakeAuditRepo{}
	service := enabledService(repo)

	recorder := performList(t, service,
		"actorId=u-1&action=user.login&appId=app-1&outcome=failure"+
			"&from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:00Z&beforeId=90&limit=10")
	require.Equal(t, http.StatusOK, recorder.Code)

	params := repo.listParams
	require.NotNil(t, params.ActorID)
	assert.Equal(t, "u-1", *params.ActorID)
	require.NotNil(t, params.Action)
	assert.Equal(t, "user.login", *params.Action)
	require.NotNil(t, params.AppID)
	assert.Equal(t, "app-1", *params.AppID)
	require.NotNil(t, params.Outcome)
	assert.Equal(t, "failure", *params.Outcome)
	require.NotNil(t, params.From)
	assert.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), params.From.UTC())
	require.NotNil(t, params.To)
	assert.Equal(t, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), params.To.UTC())
	require.NotNil(t, params.BeforeID)
	assert.Equal(t, int64(90), *params.BeforeID)
	// The service asks one extra row to detect the next page.
	assert.Equal(t, 11, params.Limit)

	// No filters: everything nil, default page size.
	performList(t, service, "")
	params = repo.listParams
	assert.Nil(t, params.ActorID)
	assert.Nil(t, params.Action)
	assert.Nil(t, params.AppID)
	assert.Nil(t, params.Outcome)
	assert.Nil(t, params.From)
	assert.Nil(t, params.To)
	assert.Nil(t, params.BeforeID)
	assert.Equal(t, DefaultPageSize+1, params.Limit)
}

func TestListHandlerResponseShape(t *testing.T) {
	occurred := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	repo := &fakeAuditRepo{listResult: []Event{{
		ID:            7,
		OccurredAt:    occurred,
		ActorType:     auditlog.ActorUser,
		ActorID:       "u-1",
		ActorDisplay:  "axel@example.com",
		Action:        auditlog.ActionAppRenamed,
		TargetType:    "app",
		TargetID:      "app-1",
		TargetDisplay: "My App",
		AppID:         "app-1",
		Outcome:       auditlog.OutcomeSuccess,
		IP:            "203.0.113.7",
		UserAgent:     "Mozilla/5.0",
		Metadata:      map[string]any{"previous_name": "Old App"},
	}}}
	service := enabledService(repo)

	recorder := performList(t, service, "")
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))

	wire := decodeList(t, recorder)
	require.Len(t, wire.Events, 1)
	assert.Equal(t, int64(1), wire.Count)
	assert.Nil(t, wire.NextCursor)
	// The wire contract: camelCase keys, RFC3339 date, metadata as an object.
	event := wire.Events[0]
	assert.Equal(t, float64(7), event["id"])
	assert.Equal(t, "2026-07-21T10:00:00Z", event["occurredAt"])
	assert.Equal(t, "user", event["actorType"])
	assert.Equal(t, "u-1", event["actorId"])
	assert.Equal(t, "axel@example.com", event["actorDisplay"])
	assert.Equal(t, "app.renamed", event["action"])
	assert.Equal(t, "app", event["targetType"])
	assert.Equal(t, "app-1", event["targetId"])
	assert.Equal(t, "My App", event["targetDisplay"])
	assert.Equal(t, "app-1", event["appId"])
	assert.Equal(t, "success", event["outcome"])
	assert.Equal(t, "203.0.113.7", event["ip"])
	assert.Equal(t, "Mozilla/5.0", event["userAgent"])
	assert.Equal(t, map[string]any{"previous_name": "Old App"}, event["metadata"])
}

func TestListHandlerPaginationWalk(t *testing.T) {
	// Newest first, like the store returns them.
	repo := &fakeAuditRepo{listResult: []Event{
		{ID: 3, Action: auditlog.ActionUserLogin},
		{ID: 2, Action: auditlog.ActionUserLogin},
		{ID: 1, Action: auditlog.ActionUserLogin},
	}}
	service := enabledService(repo)

	firstPage := decodeList(t, performList(t, service, "limit=2"))
	require.Len(t, firstPage.Events, 2)
	assert.Equal(t, float64(3), firstPage.Events[0]["id"])
	assert.Equal(t, float64(2), firstPage.Events[1]["id"])
	require.NotNil(t, firstPage.NextCursor)
	assert.Equal(t, int64(2), *firstPage.NextCursor)

	// The client sends the cursor back verbatim: last page, no overlap.
	secondPage := decodeList(t, performList(t, service, "limit=2&beforeId=2"))
	require.Len(t, secondPage.Events, 1)
	assert.Equal(t, float64(1), secondPage.Events[0]["id"])
	assert.Nil(t, secondPage.NextCursor)
}

func TestListHandlerRejectsInvalidInput(t *testing.T) {
	service := enabledService(&fakeAuditRepo{})
	invalidQueries := map[string]string{
		"malformed from": "from=notadate",
		"malformed to":   "to=notadate",
		"inverted range": "from=2026-02-01T00:00:00Z&to=2026-01-01T00:00:00Z",
		"invalid limit":  "limit=abc",
		"invalid cursor": "beforeId=zzz",
	}
	for name, query := range invalidQueries {
		t.Run(name, func(t *testing.T) {
			recorder := performList(t, service, query)
			assert.Equal(t, http.StatusBadRequest, recorder.Code, "query %q", query)
		})
	}
}

func TestListHandlerStatelessMode(t *testing.T) {
	service := NewAuditService(nil)
	service.licenseValid = func() bool { return true }

	recorder := performList(t, service, "")
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "stateless")
}

func TestListHandlerEmptyPageSerializesEmptyArray(t *testing.T) {
	service := enabledService(&fakeAuditRepo{})

	recorder := performList(t, service, "")
	require.Equal(t, http.StatusOK, recorder.Code)
	// [] and not null: the dashboard iterates without a nil check.
	assert.Contains(t, recorder.Body.String(), `"events":[]`)
}

func TestListHandlerRepositoryFailureIsAnInternalError(t *testing.T) {
	repo := &fakeAuditRepo{listErr: errors.New("database down")}
	service := enabledService(repo)

	recorder := performList(t, service, "")
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	// The infrastructure detail stays server-side.
	assert.NotContains(t, recorder.Body.String(), "database down")
}

func TestListHandlerReadsStayOpenWithoutLicense(t *testing.T) {
	// A lapsed license stops collection, never read access: the viewer must
	// keep answering (the UI overlay is the gate, not the server).
	repo := &fakeAuditRepo{listResult: []Event{{ID: 1, Action: auditlog.ActionUserLogin}}}
	service := NewAuditService(repo)
	service.licenseValid = func() bool { return false }

	recorder := performList(t, service, "")
	require.Equal(t, http.StatusOK, recorder.Code)
	wire := decodeList(t, recorder)
	require.Len(t, wire.Events, 1)
}
