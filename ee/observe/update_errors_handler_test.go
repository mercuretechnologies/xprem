// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"xprem/ee/licensing"
)

type recordingUpdateErrorsReader struct {
	ExplorerReader
	appID, updateID string
	limit, offset   int
	calls           int
	deadline        time.Time
	err             error
}

func (reader *recordingUpdateErrorsReader) ReadUpdateErrors(ctx context.Context, appID, updateID string, limit, offset int) (UpdateErrorsPage, error) {
	reader.calls++
	reader.appID, reader.updateID, reader.limit, reader.offset = appID, updateID, limit, offset
	reader.deadline, _ = ctx.Deadline()
	return UpdateErrorsPage{Available: true, UpdateID: updateID, Limit: limit, Offset: offset, Errors: []UpdateErrorSummary{}}, reader.err
}

func serveUpdateErrors(handler *ExplorerHandler, appID, updateID, query string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	router.HandleFunc("/apps/{APP_ID}/observe/updates/{UPDATE_ID}/errors", handler.GetUpdateErrorsHandler)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/apps/"+appID+"/observe/updates/"+updateID+"/errors"+query, nil))
	return response
}

func TestUpdateErrorsHandlerRechecksEnterpriseLicense(t *testing.T) {
	previous := licensing.Current()
	t.Cleanup(func() {
		if previous == nil {
			licensing.Deactivate()
		} else {
			licensing.Activate(*previous)
		}
	})
	reader := &recordingUpdateErrorsReader{}
	handler := NewExplorerHandler(reader, nil)
	appID, updateID := uuid.NewString(), uuid.NewString()
	licensing.Activate(licensing.License{PlanCode: licensing.PlanEnterprise})
	require.Equal(t, http.StatusOK, serveUpdateErrors(handler, appID, updateID, "").Code)
	licensing.Deactivate()
	require.Equal(t, http.StatusForbidden, serveUpdateErrors(handler, appID, updateID, "").Code)
	require.Equal(t, 1, reader.calls, "license revocation must prevent reads even when a previous answer was cached")
}

func TestUpdateErrorsHandlerPaginationAndDeadline(t *testing.T) {
	reader := &recordingUpdateErrorsReader{}
	handler := NewExplorerHandler(reader, nil)
	handler.licenseValid = func() bool { return true }
	appID, updateID := uuid.NewString(), uuid.NewString()
	response := serveUpdateErrors(handler, strings.ToUpper(appID), strings.ToUpper(updateID), "?limit=100&offset=10000")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, appID, reader.appID)
	require.Equal(t, updateID, reader.updateID)
	require.Equal(t, 100, reader.limit)
	require.Equal(t, 10000, reader.offset)
	require.False(t, reader.deadline.IsZero())
	require.WithinDuration(t, time.Now().Add(telemetryReadTimeout), reader.deadline, time.Second)
	require.Equal(t, http.StatusOK, serveUpdateErrors(handler, appID, updateID, "").Code)
	require.Equal(t, 25, reader.limit)
	require.Zero(t, reader.offset)
}

func TestUpdateErrorsHandlerRejectsInvalidInputBeforeRead(t *testing.T) {
	reader := &recordingUpdateErrorsReader{}
	handler := NewExplorerHandler(reader, nil)
	handler.licenseValid = func() bool { return true }
	appID, updateID := uuid.NewString(), uuid.NewString()
	for _, query := range []string{"?limit=0", "?limit=-1", "?limit=101", "?limit=x", "?offset=-1", "?offset=10001", "?offset=x"} {
		require.Equal(t, http.StatusBadRequest, serveUpdateErrors(handler, appID, updateID, query).Code, query)
	}
	for _, id := range []string{"invalid", ZeroUpdateID} {
		require.Equal(t, http.StatusBadRequest, serveUpdateErrors(handler, appID, id, "").Code)
		require.Equal(t, http.StatusBadRequest, serveUpdateErrors(handler, id, updateID, "").Code)
	}
	require.Zero(t, reader.calls)
}

func TestUpdateErrorsHandlerUnavailableAndPrivateErrors(t *testing.T) {
	appID, updateID := uuid.NewString(), uuid.NewString()
	for _, reader := range []ExplorerReader{nil, (*Explorer)(nil)} {
		handler := NewExplorerHandler(reader, nil)
		handler.licenseValid = func() bool { return true }
		response := serveUpdateErrors(handler, appID, updateID, "")
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var page UpdateErrorsPage
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
		require.False(t, page.Available)
		require.Equal(t, updateID, page.UpdateID)
		require.Equal(t, 25, page.Limit)
		require.NotNil(t, page.Errors)
	}
	reader := &recordingUpdateErrorsReader{err: errors.New("private database failure")}
	handler := NewExplorerHandler(reader, nil)
	handler.licenseValid = func() bool { return true }
	response := serveUpdateErrors(handler, appID, updateID, "")
	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.NotContains(t, response.Body.String(), reader.err.Error())
	reader.err = ErrInvalidErrorsQuery
	require.Equal(t, http.StatusBadRequest, serveUpdateErrors(handler, appID, updateID, "").Code)
}
