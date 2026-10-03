// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/stretchr/testify/require"
	"log"
	"net/http"
	"testing"
	"xprem/ee/licensing"
)

func TestErrorsHandlerRechecksLicenseBeforeReader(t *testing.T) {
	reader := &recordingExplorer{}
	handler := NewExplorerHandler(reader, nil)
	valid := true
	checks := 0
	handler.licenseValid = func() bool { checks++; return valid }
	require.Equal(t, http.StatusOK, serveExplorer(handler, "/observe/errors").Code)
	valid = false
	for _, path := range []string{"/observe/errors", "/observe/errors/groups/error-id"} {
		require.Equal(t, http.StatusForbidden, serveExplorer(handler, path).Code)
	}
	require.Equal(t, 3, checks)
	require.Equal(t, 1, reader.errorsCalls, "a reader may cache the initial answer; loss of license must prevent subsequent reads")
}

func TestErrorsHandlerSharedFiltersAndPagination(t *testing.T) {
	reader := &recordingExplorer{}
	handler := NewExplorerHandler(reader, nil)
	handler.licenseValid = func() bool { return true }
	response := serveExplorer(handler, "/observe/errors?from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:00Z&platform=ios&deviceModel=iPhone18%2C2&fatality=non_fatal&search=TypeError&sort=impactedDevices&limit=25&offset=75&includeSeries=true")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "nonfatal", reader.errorsQuery.Fatality)
	require.Equal(t, "TypeError", reader.errorsQuery.Search)
	require.Equal(t, "impactedDevices", reader.errorsQuery.Sort)
	require.Equal(t, 25, reader.errorsQuery.Limit)
	require.Equal(t, 75, reader.errorsQuery.Offset)
	require.True(t, reader.errorsQuery.IncludeSeries)
	require.Equal(t, []string{"ios"}, reader.errorsQuery.Platform)
	require.Equal(t, []string{"iPhone18,2"}, reader.errorsQuery.DeviceModels)
	response = serveExplorer(handler, "/observe/errors/groups/group-id?from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:00Z&platform=ios&fatality=fatal&limit=25")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "group-id", reader.errorDetailsID)
	require.Equal(t, "fatal", reader.errorDetailsQuery.Fatality)
	require.Equal(t, 25, reader.errorDetailsQuery.Limit)
	require.Equal(t, reader.errorsQuery.From, reader.errorDetailsQuery.From)
	require.Equal(t, reader.errorsQuery.To, reader.errorDetailsQuery.To)
	require.Equal(t, reader.errorsQuery.Platform, reader.errorDetailsQuery.Platform)
}

func TestErrorsHandlersRejectInvalidQueriesBeforeRead(t *testing.T) {
	reader := &recordingExplorer{}
	handler := NewExplorerHandler(reader, nil)
	handler.licenseValid = func() bool { return true }
	for _, suffix := range []string{
		"?from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:01Z",
		"?limit=101", "?limit=-1", "?fatality=maybe", "?platform=windows", "?updateId=invalid", "?thermalState=serious",
	} {
		for _, path := range []string{"/observe/errors", "/observe/errors/groups/group-id"} {
			t.Run(path+suffix, func(t *testing.T) { require.Equal(t, http.StatusBadRequest, serveExplorer(handler, path+suffix).Code) })
		}
	}
	for _, suffix := range []string{"?offset=10001", "?offset=-1", "?sort=invalid", "?includeSeries=maybe"} {
		require.Equal(t, http.StatusBadRequest, serveExplorer(handler, "/observe/errors"+suffix).Code)
	}
	require.Equal(t, http.StatusBadRequest, serveExplorer(handler, "/observe/errors/groups/group-id?cursor=invalid!!").Code)
	require.Zero(t, reader.errorsCalls)
}

func TestErrorsHandlerUsesCurrentLicenseOnEachRequest(t *testing.T) {
	previous := licensing.Current()
	t.Cleanup(func() {
		if previous == nil {
			licensing.Deactivate()
		} else {
			licensing.Activate(*previous)
		}
	})
	reader := &recordingExplorer{}
	handler := NewExplorerHandler(reader, nil)
	licensing.Activate(licensing.License{PlanCode: licensing.PlanEnterprise})
	require.Equal(t, http.StatusOK, serveExplorer(handler, "/observe/errors").Code)
	licensing.Deactivate()
	require.Equal(t, http.StatusForbidden, serveExplorer(handler, "/observe/errors").Code)
	require.Equal(t, 1, reader.errorsCalls)
}

// Embedding the interface leaves unrelated reads unused while exercising both
// error endpoints against the same reader failure.
type failingErrorsExplorer struct {
	ExplorerReader
	err error
}

func (reader failingErrorsExplorer) ReadErrors(context.Context, string, ErrorsQuery) (ErrorsPage, error) {
	return ErrorsPage{}, reader.err
}

func (reader failingErrorsExplorer) ReadErrorDetails(context.Context, string, string, ErrorDetailsQuery) (ErrorDetails, error) {
	return ErrorDetails{}, reader.err
}

func TestErrorsHandlersKeepReaderFailuresPrivateAndLogOnlyUnexpectedErrors(t *testing.T) {
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"error ID", ErrInvalidErrorID, http.StatusBadRequest},
		{"errors query", ErrInvalidErrorsQuery, http.StatusBadRequest},
		{"range", errInvalidObserveRange, http.StatusBadRequest},
		{"platform", errInvalidObservePlatform, http.StatusBadRequest},
		{"observe filter", errInvalidObserveFilter, http.StatusBadRequest},
		{"identity filter", errInvalidIdentityFilter, http.StatusBadRequest},
		{"identity cohort", errObserveCohortTooLarge, http.StatusBadRequest},
		{"unexpected", errors.New("database failed"), http.StatusInternalServerError},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			privateDetail := "private database or filter detail"
			handler := NewExplorerHandler(failingErrorsExplorer{err: fmt.Errorf("%w: %s", testCase.err, privateDetail)}, nil)
			handler.licenseValid = func() bool { return true }
			for _, path := range []string{"/observe/errors", "/observe/errors/groups/group-id"} {
				logs.Reset()
				response := serveExplorer(handler, path)
				require.Equal(t, testCase.status, response.Code, response.Body.String())
				require.NotContains(t, response.Body.String(), privateDetail)
				if testCase.status == http.StatusBadRequest {
					require.Empty(t, logs.String(), "reader validation failures must not be logged")
				} else {
					require.Contains(t, response.Body.String(), "An internal error occurred.")
					require.Contains(t, logs.String(), privateDetail, "unexpected failures remain available in server logs")
				}
			}
		})
	}
}
