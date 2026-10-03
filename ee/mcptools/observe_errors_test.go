// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	"errors"
	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
	"xprem/ee/licensing"
	"xprem/ee/observe"
	mittools "xprem/internal/mcptools"
	"xprem/internal/services"
)

type errorExplorerFake struct {
	observe.ExplorerReader
	errorsCalls  int
	detailsCalls int
	appID        string
	errorID      string
	query        observe.ErrorsQuery
	detailsQuery observe.ErrorDetailsQuery
}

func (reader *errorExplorerFake) ReadErrors(_ context.Context, appID string, query observe.ErrorsQuery) (observe.ErrorsPage, error) {
	reader.errorsCalls++
	reader.appID, reader.query = appID, query
	return observe.ErrorsPage{Available: true, Errors: []observe.ErrorSummary{{Occurrences: 150, ImpactedDevices: 1, CrashOccurrences: 50}}}, nil
}

func (reader *errorExplorerFake) ReadErrorDetails(_ context.Context, appID, errorID string, query observe.ErrorDetailsQuery) (observe.ErrorDetails, error) {
	reader.detailsCalls++
	reader.appID, reader.errorID, reader.detailsQuery = appID, errorID, query
	return observe.ErrorDetails{Series: []observe.ObserveEventPoint{{Timestamp: time.Now(), Count: 150}}}, nil
}

func TestErrorsToolsRecheckAppAccessAndLicenseBeforeReader(t *testing.T) {
	deps := healthDeps()
	reader := &errorExplorerFake{}
	deps.Explorer = reader
	licensed := true
	checks := 0
	deps.LicenseValid = func() bool { checks++; return licensed }
	ctx, req := context.Background(), auditRequestFor(healthPrincipal)
	_, _, err := queryErrorsHandler(deps)(ctx, req, QueryErrorsInput{AppId: "app-1"})
	require.NoError(t, err)
	licensed = false
	_, _, err = queryErrorsHandler(deps)(ctx, req, QueryErrorsInput{AppId: "app-1"})
	require.ErrorContains(t, err, "Enterprise license")
	_, _, err = getErrorDetailsHandler(deps)(ctx, req, GetErrorDetailsInput{AppId: "app-1", ErrorId: "id"})
	require.ErrorContains(t, err, "Enterprise license")
	require.Equal(t, 3, checks)
	require.Equal(t, 1, reader.errorsCalls)
	require.Zero(t, reader.detailsCalls)
	licensed = true
	deps.Authorize = func(_ context.Context, _ *services.DashboardPrincipal, appID string, access mittools.Access) error {
		require.Equal(t, "app-1", appID)
		require.Equal(t, "observe:read", access.Perm)
		return errors.New("permission denied")
	}
	_, _, err = queryErrorsHandler(deps)(ctx, req, QueryErrorsInput{AppId: "app-1"})
	require.ErrorContains(t, err, "permission denied")
	_, _, err = getErrorDetailsHandler(deps)(ctx, req, GetErrorDetailsInput{AppId: "app-1", ErrorId: "id"})
	require.ErrorContains(t, err, "permission denied")
	_, _, err = queryErrorsHandler(deps)(ctx, req, QueryErrorsInput{AppId: "another-app"})
	require.Error(t, err)
	require.Equal(t, 1, reader.errorsCalls)
	require.Zero(t, reader.detailsCalls)
}

func TestErrorsToolsShareFiltersPaginationAndServerCounts(t *testing.T) {
	deps := healthDeps()
	reader := &errorExplorerFake{}
	deps.Explorer = reader
	deps.LicenseValid = func() bool { return true }
	ctx, req := context.Background(), auditRequestFor(healthPrincipal)
	filters := ObserveFilters{Platforms: []string{"ios"}, DeviceModels: []string{"iPhone18,2"}}
	_, output, err := queryErrorsHandler(deps)(ctx, req, QueryErrorsInput{
		AppId: "app-1", ObserveFilters: filters, From: "2026-01-01T00:00:00Z", To: "2026-02-01T00:00:00Z",
		Fatality: "non_fatal", Search: "TypeError", Sort: "impactedDevices", Limit: 25, Offset: 75, IncludeSeries: true,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(150), output.Errors[0].Occurrences)
	require.Equal(t, uint64(1), output.Errors[0].ImpactedDevices)
	require.Equal(t, uint64(50), output.Errors[0].CrashOccurrences)
	require.Equal(t, "nonfatal", reader.query.Fatality)
	require.Equal(t, "TypeError", reader.query.Search)
	require.Equal(t, 25, reader.query.Limit)
	require.Equal(t, 75, reader.query.Offset)
	require.True(t, reader.query.IncludeSeries)
	require.Equal(t, filters.DeviceModels, reader.query.DeviceModels)
	_, details, err := getErrorDetailsHandler(deps)(ctx, req, GetErrorDetailsInput{
		AppId: "app-1", ErrorId: "opaque-id", ObserveFilters: filters,
		From: "2026-01-01T00:00:00Z", To: "2026-02-01T00:00:00Z", Limit: 25,
	})
	require.NoError(t, err)
	require.Empty(t, details.Series, "MCP series must be opt-in")
	require.Equal(t, "opaque-id", reader.errorID)
	require.Equal(t, reader.query.ExplorerQuery, reader.detailsQuery.ExplorerQuery)
	_, details, err = getErrorDetailsHandler(deps)(ctx, req, GetErrorDetailsInput{AppId: "app-1", ErrorId: "opaque-id", IncludeSeries: true})
	require.NoError(t, err)
	require.Len(t, details.Series, 1)
}

func TestErrorsToolsRejectInvalidQueriesBeforeReader(t *testing.T) {
	deps := healthDeps()
	reader := &errorExplorerFake{}
	deps.Explorer = reader
	deps.LicenseValid = func() bool { return true }
	ctx, req := context.Background(), auditRequestFor(healthPrincipal)
	for _, input := range []QueryErrorsInput{
		{Limit: 101}, {Limit: -1}, {Offset: -1}, {Offset: 10001}, {Fatality: "maybe"}, {Sort: "invalid"},
		{Search: strings.Repeat("x", 257)}, {From: "2026-01-01T00:00:00Z", To: "2026-02-01T00:00:01Z"},
		{ObserveFilters: ObserveFilters{UpdateIds: []string{"not-a-uuid"}}},
		{ObserveFilters: ObserveFilters{ThermalState: []string{"serious"}}},
	} {
		input.AppId = "app-1"
		_, _, err := queryErrorsHandler(deps)(ctx, req, input)
		require.Error(t, err)
	}
	for _, input := range []GetErrorDetailsInput{
		{Limit: 101}, {Limit: -1}, {Fatality: "maybe"}, {Cursor: "bad-cursor!!"},
		{From: "2026-01-01T00:00:00Z", To: "2026-02-01T00:00:01Z"},
	} {
		input.AppId, input.ErrorId = "app-1", "opaque-id"
		_, _, err := getErrorDetailsHandler(deps)(ctx, req, input)
		require.Error(t, err)
	}
	require.Zero(t, reader.errorsCalls)
	require.Zero(t, reader.detailsCalls)
}

func TestErrorsToolsUseCurrentLicenseOnEachCall(t *testing.T) {
	previous := licensing.Current()
	t.Cleanup(func() {
		if previous == nil {
			licensing.Deactivate()
		} else {
			licensing.Activate(*previous)
		}
	})
	reader := &errorExplorerFake{}
	deps := healthDeps()
	deps.Explorer = reader
	licensing.Activate(licensing.License{PlanCode: licensing.PlanEnterprise})
	ctx, req := context.Background(), auditRequestFor(healthPrincipal)
	_, _, err := queryErrorsHandler(deps)(ctx, req, QueryErrorsInput{AppId: "app-1"})
	require.NoError(t, err)
	licensing.Deactivate()
	_, _, err = queryErrorsHandler(deps)(ctx, req, QueryErrorsInput{AppId: "app-1"})
	require.ErrorContains(t, err, "Enterprise license")
	require.Equal(t, 1, reader.errorsCalls)
}

func TestErrorsToolsDeclareReadOnly(t *testing.T) {
	ctx := context.Background()
	server := mcpprot.NewServer(&mcpprot.Implementation{Name: "test", Version: "0"}, nil)
	registerQueryErrors(server, Deps{})
	registerGetErrorDetails(server, Deps{})
	clientTransport, serverTransport := mcpprot.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	defer serverSession.Close()
	clientSession, err := mcpprot.NewClient(&mcpprot.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer clientSession.Close()
	list, err := clientSession.ListTools(ctx, nil)
	require.NoError(t, err)
	require.Len(t, list.Tools, 2)
	for _, tool := range list.Tools {
		require.NotNil(t, tool.Annotations)
		require.True(t, tool.Annotations.ReadOnlyHint, tool.Name)
	}
}
