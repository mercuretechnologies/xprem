// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xprem/ee/observe"
	"xprem/ee/rbac"
	mittools "xprem/internal/mcptools"
	"xprem/internal/services"
)

// Without telemetry every explorer tool says so, rather than dereferencing a
// nil explorer.
func TestObserveToolsWithoutTelemetry(t *testing.T) {
	deps := healthDeps()
	ctx := context.Background()
	req := auditRequestFor(healthPrincipal)

	calls := map[string]func() error{
		"query_logs": func() error {
			_, _, err := queryLogsHandler(deps)(ctx, req, QueryLogsInput{AppId: "app-1"})
			return err
		},
		"get_observe_overview": func() error {
			_, _, err := getObserveOverviewHandler(deps)(ctx, req, GetObserveOverviewInput{AppId: "app-1"})
			return err
		},
		"get_metric_breakdown": func() error {
			_, _, err := getMetricBreakdownHandler(deps)(ctx, req, GetMetricBreakdownInput{AppId: "app-1", Metric: "cold-launch", Dimension: "deviceModel"})
			return err
		},
		"get_observe_events": func() error {
			_, _, err := getObserveEventsHandler(deps)(ctx, req, GetObserveEventsInput{AppId: "app-1"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil || !strings.Contains(err.Error(), "collects no telemetry") {
				t.Fatalf("expected the no-telemetry answer, got %v", err)
			}
		})
	}
}

// Holding a permission on one app must not unlock another: the session-level
// visibility of a tool is not an authorization, and every ee tool re-checks
// the permission on the app it was asked about.
func TestObserveAndDeviceToolsAuthorizePerApp(t *testing.T) {
	deps := healthDeps()
	deps.Explorer = &observe.Explorer{}
	var asked []mittools.Access
	deps.Authorize = func(_ context.Context, _ *services.DashboardPrincipal, appID string, access mittools.Access) error {
		asked = append(asked, access)
		if appID != "app-1" {
			t.Fatalf("the tool must authorize the app it was asked about, got %q", appID)
		}
		return errors.New("permission denied on this app")
	}
	ctx := context.Background()
	req := auditRequestFor(healthPrincipal)

	calls := map[string]struct {
		perm string
		call func() error
	}{
		"query_logs": {"observe:read", func() error {
			_, _, err := queryLogsHandler(deps)(ctx, req, QueryLogsInput{AppId: "app-1"})
			return err
		}},
		"get_observe_overview": {"observe:read", func() error {
			_, _, err := getObserveOverviewHandler(deps)(ctx, req, GetObserveOverviewInput{AppId: "app-1"})
			return err
		}},
		"get_metric_breakdown": {"observe:read", func() error {
			_, _, err := getMetricBreakdownHandler(deps)(ctx, req, GetMetricBreakdownInput{AppId: "app-1", Metric: "cold-launch", Dimension: "deviceModel"})
			return err
		}},
		"get_observe_events": {"observe:read", func() error {
			_, _, err := getObserveEventsHandler(deps)(ctx, req, GetObserveEventsInput{AppId: "app-1"})
			return err
		}},
		"search_devices": {"identity:read", func() error {
			_, _, err := searchDevicesHandler(deps)(ctx, req, SearchDevicesInput{AppId: "app-1"})
			return err
		}},
		"get_device": {"identity:read", func() error {
			_, _, err := getDeviceHandler(deps)(ctx, req, GetDeviceInput{AppId: "app-1", EasClientId: uuidA})
			return err
		}},
		"count_online_devices": {"identity:read", func() error {
			_, _, err := countOnlineDevicesHandler(deps)(ctx, req, CountOnlineDevicesInput{AppId: "app-1"})
			return err
		}},
		"get_device_attributes": {"identity:read", func() error {
			_, _, err := getDeviceAttributesHandler(deps)(ctx, req, GetDeviceAttributesInput{AppId: "app-1"})
			return err
		}},
	}
	for name, testCase := range calls {
		t.Run(name, func(t *testing.T) {
			asked = nil
			err := testCase.call()
			if err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Fatalf("a denied permission must refuse the call, got %v", err)
			}
			if len(asked) != 1 || asked[0].Perm != testCase.perm {
				t.Fatalf("expected one authorization on %s, got %+v", testCase.perm, asked)
			}
		})
	}
}

// A missing Authorize seam must fail closed rather than serve the data.
func TestEeToolsFailClosedWithoutAuthorize(t *testing.T) {
	deps := healthDeps()
	deps.Explorer = &observe.Explorer{}
	deps.Authorize = nil
	if _, _, err := queryLogsHandler(deps)(context.Background(), auditRequestFor(healthPrincipal), QueryLogsInput{AppId: "app-1"}); err == nil {
		t.Fatal("expected a refusal when authorization is not wired")
	}
}

func TestQueryLogsValidatesItsInput(t *testing.T) {
	deps := healthDeps()
	deps.Explorer = &observe.Explorer{}
	ctx := context.Background()
	req := auditRequestFor(healthPrincipal)

	if _, _, err := queryLogsHandler(deps)(ctx, req, QueryLogsInput{AppId: "app-1", Severity: "critical"}); err == nil || !strings.Contains(err.Error(), "severity must be") {
		t.Fatalf("an unknown severity must be named, got %v", err)
	}
	if _, _, err := queryLogsHandler(deps)(ctx, req, QueryLogsInput{AppId: "app-1", Search: strings.Repeat("x", 257)}); err == nil {
		t.Fatal("an oversized search must be refused")
	}
	if _, _, err := queryLogsHandler(deps)(ctx, req, QueryLogsInput{AppId: "app-1", Cursor: "not-a-cursor!!"}); err == nil {
		t.Fatal("a malformed cursor must be refused")
	}
	// Logs keep a shorter window than the aggregates.
	if _, _, err := queryLogsHandler(deps)(ctx, req, QueryLogsInput{
		AppId: "app-1", From: "2026-01-01T00:00:00Z", To: "2026-07-30T00:00:00Z",
	}); err == nil || !strings.Contains(err.Error(), "too wide") {
		t.Fatalf("expected the window ceiling, got %v", err)
	}
}

// A breakdown only knows the built-in timings; a custom metric must be told
// so, not answered with an opaque failure.
func TestMetricBreakdownValidatesMetricAndDimension(t *testing.T) {
	deps := healthDeps()
	deps.Explorer = &observe.Explorer{}
	ctx := context.Background()
	req := auditRequestFor(healthPrincipal)

	_, _, err := getMetricBreakdownHandler(deps)(ctx, req, GetMetricBreakdownInput{
		AppId: "app-1", Metric: "my.custom.metric", Dimension: "deviceModel",
	})
	if err == nil || !strings.Contains(err.Error(), "built-in timings") {
		t.Fatalf("a custom metric must be named as unsplittable, got %v", err)
	}
	_, _, err = getMetricBreakdownHandler(deps)(ctx, req, GetMetricBreakdownInput{
		AppId: "app-1", Metric: "cold-launch", Dimension: "nope",
	})
	if err == nil || !strings.Contains(err.Error(), "dimension must be one of") {
		t.Fatalf("an unknown dimension must list the valid ones, got %v", err)
	}
	// The listed dimensions are the ones the server actually accepts.
	for _, dimension := range observe.BreakdownDimensions() {
		if !observe.IsBreakdownDimension(dimension) {
			t.Errorf("%s is advertised but not accepted", dimension)
		}
	}
}

func TestOverviewSeriesNeedsNamedMetrics(t *testing.T) {
	deps := healthDeps()
	deps.Explorer = &observe.Explorer{}
	_, _, err := getObserveOverviewHandler(deps)(context.Background(), auditRequestFor(healthPrincipal), GetObserveOverviewInput{
		AppId: "app-1", IncludeSeries: true,
	})
	if err == nil || !strings.Contains(err.Error(), "needs metricIds") {
		t.Fatalf("curves for every metric would flood the answer, got %v", err)
	}
}

// The bucket is derived from the window, never asked for: a wide window cannot
// request a million points.
func TestObserveBucketGrowsWithTheWindow(t *testing.T) {
	cases := []struct {
		window time.Duration
		bucket time.Duration
	}{
		{time.Hour, 5 * time.Minute},
		{24 * time.Hour, 15 * time.Minute},
		{7 * 24 * time.Hour, time.Hour},
		{30 * 24 * time.Hour, 6 * time.Hour},
		{90 * 24 * time.Hour, 24 * time.Hour},
	}
	for _, testCase := range cases {
		if got := observe.Bucket(testCase.window); got != testCase.bucket {
			t.Errorf("window %v: expected %v, got %v", testCase.window, testCase.bucket, got)
		}
	}
}

func TestObserveFiltersMapOntoTheExplorerQuery(t *testing.T) {
	deps := healthDeps()
	query, err := deps.explorerQuery(context.Background(), "app-1", ObserveFilters{
		Platforms:    []string{"ios"},
		Branches:     []string{"main"},
		ThermalState: []string{"serious"},
	}, "", "", maxObserveWindow)
	if err != nil {
		t.Fatal(err)
	}
	if len(query.Platform) != 1 || query.Platform[0] != "ios" {
		t.Errorf("platforms did not map: %+v", query.Platform)
	}
	if len(query.Conditions["thermalState"]) != 1 {
		t.Errorf("conditions did not map: %+v", query.Conditions)
	}
	if query.Bucket != 15*time.Minute {
		t.Errorf("the default 24h window must bucket at 15 minutes, got %v", query.Bucket)
	}
}

func TestLogBodyIsTruncated(t *testing.T) {
	long := strings.Repeat("x", maxLogFieldLength+500)
	if truncated := truncate(long); len(truncated) >= len(long) || !strings.HasSuffix(truncated, "(truncated)") {
		t.Errorf("a long body must be cut and say so, got %d chars", len(truncated))
	}
	if truncate("short") != "short" {
		t.Error("a short body must pass through untouched")
	}
}

// Every permission this table gates on exists in the catalog, and carries the
// fallback the catalog says it does: MCPAccess derives both, so a drift here
// would mean a tool gating differently from its route.
func TestDeclaredPermissionsAreCatalogPermissions(t *testing.T) {
	for _, perm := range DeclaredPermissions() {
		if !rbac.IsValidPermission(perm) {
			t.Errorf("%s is not a catalog permission", perm)
		}
	}
	if identityAccess.Perm != string(rbac.PermIdentityRead) || identityAccess.Fallback != mittools.FallbackAnyMember {
		t.Errorf("the device tools must gate like their route: %+v", identityAccess)
	}
	if observeAccess.Perm != string(rbac.PermObserveRead) || observeAccess.Fallback != mittools.FallbackAnyMember {
		t.Errorf("the explorer tools must gate like their route: %+v", observeAccess)
	}
}

func TestObserveToolsAreVisibleAndReadOnly(t *testing.T) {
	deps := healthDeps()
	deps.CanUseSomewhere = func(_ context.Context, _ *services.DashboardPrincipal, access mittools.Access) bool {
		return access.Perm == "observe:read" && access.Fallback == mittools.FallbackAnyMember
	}
	tools := registeredTools(t, deps, healthPrincipal)
	for _, name := range []string{"query_logs", "get_observe_overview", "get_metric_breakdown", "get_observe_events"} {
		if !tools[name] {
			t.Errorf("%s must be registered for a principal holding observe:read", name)
		}
	}
}
