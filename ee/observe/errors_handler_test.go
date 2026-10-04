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
	"xprem/ee/symbolication"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeErrorReader struct {
	group *ErrorGroup
	calls *int
}

func (r fakeErrorReader) ReadErrorGroup(context.Context, string, string, string) (*ErrorGroup, error) {
	if r.calls != nil {
		(*r.calls)++
	}
	return r.group, nil
}

type indexStateFunc func() error

func (f indexStateFunc) UpdateIndexState(context.Context, string, string) error { return f() }
func (f indexStateFunc) Available() bool                                        { return true }

type availableIndexState struct {
	indexStateFunc
	enabled bool
}

func (s availableIndexState) Available() bool { return s.enabled }

func askErrorGroup(t *testing.T, handler *ErrorsHandler, query string) ErrorGroupAnswer {
	t.Helper()
	router := mux.NewRouter()
	router.HandleFunc("/apps/{APP_ID}/observe/errors/{FINGERPRINT}", handler.GetErrorGroupHandler)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/apps/app-1/observe/errors/"+uuid.NewString()+query, nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var answer ErrorGroupAnswer
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &answer))
	return answer
}

func TestErrorGroupStatusSaysWhyAGroupIsMissing(t *testing.T) {
	updateQuery := "?updateId=" + uuid.NewString()
	for _, tc := range []struct {
		name   string
		state  error
		status ErrorGroupStatus
	}{
		{"index ready, sweep not passed", nil, ErrorGroupWaiting},
		{"index pending", symbolication.ErrIndexNotReady, ErrorGroupIndexing},
		{"index failed", symbolication.ErrIndexFailed, ErrorGroupIndexFailed},
		{"published without a map", symbolication.ErrNoSourcemap, ErrorGroupNoSourcemap},
		{"no license", symbolication.ErrUnavailable, ErrorGroupUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewErrorsHandler(fakeErrorReader{}, indexStateFunc(func() error { return tc.state }))
			answer := askErrorGroup(t, handler, updateQuery)
			assert.Equal(t, tc.status, answer.Status)
			assert.Nil(t, answer.Group)
		})
	}
}

func TestStoredErrorGroupDoesNotRecheckIndexState(t *testing.T) {
	group := &ErrorGroup{Fingerprint: uuid.NewString(), Culprit: "LabScreen.tsx in onPress"}
	indexReads := 0
	handler := NewErrorsHandler(fakeErrorReader{group: group}, indexStateFunc(func() error {
		indexReads++
		return symbolication.ErrIndexFailed
	}))
	answer := askErrorGroup(t, handler, "?updateId="+uuid.NewString())
	assert.Equal(t, ErrorGroupReady, answer.Status)
	require.NotNil(t, answer.Group)
	assert.Equal(t, "LabScreen.tsx in onPress", answer.Group.Culprit)
	assert.Zero(t, indexReads, "a stored group remains usable after an index rebuild fails")
}

func TestStoredErrorGroupRechecksAvailabilityOnEveryRequest(t *testing.T) {
	reads := 0
	indexes := &availableIndexState{
		indexStateFunc: func() error { t.Fatal("stored groups must not open or check an index"); return nil },
		enabled:        true,
	}
	handler := NewErrorsHandler(fakeErrorReader{group: &ErrorGroup{Culprit: "private-source.ts"}, calls: &reads}, indexes)
	query := "?updateId=" + uuid.NewString()
	answer := askErrorGroup(t, handler, query)
	require.Equal(t, ErrorGroupReady, answer.Status)
	require.NotNil(t, answer.Group)
	require.Equal(t, 1, reads)

	indexes.enabled = false
	answer = askErrorGroup(t, handler, query)
	assert.Equal(t, ErrorGroupUnavailable, answer.Status)
	assert.Nil(t, answer.Group)
	assert.Equal(t, 1, reads, "loss of availability must prevent another read of stored source details")
}

func TestStoredErrorGroupWithoutIndexingIsUnavailable(t *testing.T) {
	for name, indexes := range map[string]IndexStateReader{
		"nil interface": nil,
		"nil service":   (*symbolication.Service)(nil),
		"disabled":      symbolication.NewService(nil, nil, nil),
	} {
		t.Run(name, func(t *testing.T) {
			reads := 0
			handler := NewErrorsHandler(fakeErrorReader{group: &ErrorGroup{Culprit: "private-source.ts"}, calls: &reads}, indexes)
			answer := askErrorGroup(t, handler, "?updateId="+uuid.NewString())
			assert.Equal(t, ErrorGroupUnavailable, answer.Status)
			assert.Nil(t, answer.Group)
			assert.Zero(t, reads, "unavailable indexing must prevent reading stored source details")
		})
	}
}

func TestErrorGroupWithoutTheControlPlaneIsUnavailable(t *testing.T) {
	var explorer *Explorer
	answer := askErrorGroup(t, NewErrorsHandler(explorer, nil), "?updateId="+uuid.NewString())
	assert.Equal(t, ErrorGroupUnavailable, answer.Status)
}

func TestErrorGroupRefusesABadUpdateID(t *testing.T) {
	router := mux.NewRouter()
	router.HandleFunc("/apps/{APP_ID}/observe/errors/{FINGERPRINT}", NewErrorsHandler(fakeErrorReader{}, nil).GetErrorGroupHandler)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/apps/app-1/observe/errors/"+uuid.NewString()+"?updateId=nope", nil))
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}
