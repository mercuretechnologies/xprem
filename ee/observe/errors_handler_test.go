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
		*r.calls++
	}
	return r.group, nil
}

type indexStateFunc func() error

func (f indexStateFunc) UpdateIndexState(context.Context, string, string) error { return f() }

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

func TestErrorGroupIsReadyOnceGrouped(t *testing.T) {
	group := &ErrorGroup{Fingerprint: uuid.NewString(), Culprit: "LabScreen.tsx in onPress"}
	handler := NewErrorsHandler(fakeErrorReader{group: group}, indexStateFunc(func() error { return nil }))
	answer := askErrorGroup(t, handler, "?updateId="+uuid.NewString())
	assert.Equal(t, ErrorGroupReady, answer.Status)
	require.NotNil(t, answer.Group)
	assert.Equal(t, "LabScreen.tsx in onPress", answer.Group.Culprit)
}

func TestStoredErrorGroupRequiresAvailableIndexing(t *testing.T) {
	for _, state := range []error{nil, symbolication.ErrUnavailable, symbolication.ErrIndexFailed} {
		calls := 0
		group := &ErrorGroup{Fingerprint: uuid.NewString(), Culprit: "private-source.ts"}
		handler := NewErrorsHandler(fakeErrorReader{group: group, calls: &calls}, indexStateFunc(func() error { return state }))
		answer := askErrorGroup(t, handler, "?updateId="+uuid.NewString())
		if state == symbolication.ErrUnavailable {
			assert.Equal(t, ErrorGroupUnavailable, answer.Status)
			assert.Nil(t, answer.Group)
			assert.Zero(t, calls, "stored source details must not be read while indexing is unavailable")
		} else {
			assert.Equal(t, ErrorGroupReady, answer.Status)
			require.NotNil(t, answer.Group)
			assert.Equal(t, 1, calls)
		}
	}
	answer := askErrorGroup(t, NewErrorsHandler(fakeErrorReader{group: &ErrorGroup{Culprit: "private-source.ts"}}, nil), "?updateId="+uuid.NewString())
	assert.Equal(t, ErrorGroupUnavailable, answer.Status)
	assert.Nil(t, answer.Group)
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
