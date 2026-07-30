// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	"testing"
	"xprem/config"
	mittools "xprem/internal/mcptools"
	"xprem/internal/services"

	"github.com/google/uuid"
)

type fakeAppLister struct{}

func (fakeAppLister) GetApps(context.Context) ([]config.AppDescriptor, error) {
	return []config.AppDescriptor{{Id: "app-1"}}, nil
}

func healthDeps() Deps {
	return Deps{
		Apps: fakeAppLister{},
		VisibleApps: func(context.Context, *services.DashboardPrincipal) (bool, map[string]bool, error) {
			return false, nil, nil
		},
		// Allows every app; the tests that care about denial override it.
		Authorize: func(context.Context, *services.DashboardPrincipal, string, mittools.Access) error {
			return nil
		},
	}
}

var healthPrincipal = &services.DashboardPrincipal{UserId: "user-1"}

const (
	uuidA = "11111111-1111-1111-1111-111111111111"
	uuidB = "22222222-2222-2222-2222-222222222222"
)

// Without any device telemetry (stateless, or DISABLE_DEVICE_TELEMETRY), the
// tool answers what the deployment can honestly say instead of failing.
func TestGetUpdateHealthWithoutTelemetry(t *testing.T) {
	_, output, err := getUpdateHealthHandler(healthDeps())(context.Background(), auditRequestFor(healthPrincipal), GetUpdateHealthInput{
		AppId: "app-1", UpdateUUIDs: []string{uuidA},
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Source != "unavailable" || output.Note == "" {
		t.Fatalf("expected an explained unavailable source, got %+v", output)
	}
	if output.Current != nil || len(output.History) != 0 || len(output.Arrivals) != 0 {
		t.Errorf("nothing should be reported without telemetry: %+v", output)
	}
	if len(output.UpdateUUIDs) != 1 || output.From == "" || output.To == "" {
		t.Errorf("the answer must still say what it covers: %+v", output)
	}
}

func TestGetUpdateHealthRejectsBadTargets(t *testing.T) {
	deps := healthDeps()
	ctx := context.Background()
	req := auditRequestFor(healthPrincipal)

	cases := map[string]GetUpdateHealthInput{
		"neither":        {AppId: "app-1"},
		"both":           {AppId: "app-1", UpdateUUIDs: []string{uuidA}, PublishGroup: uuidB},
		"not a uuid":     {AppId: "app-1", UpdateUUIDs: []string{"12"}},
		"group not uuid": {AppId: "app-1", PublishGroup: "not-a-uuid"},
		"unknown app":    {AppId: "nope", UpdateUUIDs: []string{uuidA}},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := getUpdateHealthHandler(deps)(ctx, req, input); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}

	// Over the twenty-id ceiling the route accepts, with real distinct uuids
	// so the refusal is the ceiling and not a parse failure.
	tooMany := make([]string, 0, 21)
	for i := 0; i < 21; i++ {
		tooMany = append(tooMany, uuid.New().String())
	}
	if _, _, err := getUpdateHealthHandler(deps)(ctx, req, GetUpdateHealthInput{AppId: "app-1", UpdateUUIDs: tooMany}); err == nil {
		t.Fatal("expected a refusal past twenty update uuids")
	}
}

func TestGetUpdateHealthWindow(t *testing.T) {
	deps := healthDeps()
	ctx := context.Background()
	req := auditRequestFor(healthPrincipal)

	// A window wider than ninety days, and an inverted one, are refused.
	if _, _, err := getUpdateHealthHandler(deps)(ctx, req, GetUpdateHealthInput{
		AppId: "app-1", UpdateUUIDs: []string{uuidA},
		From: "2026-01-01T00:00:00Z", To: "2026-07-30T00:00:00Z",
	}); err == nil {
		t.Fatal("expected a refusal past the ninety-day window")
	}
	if _, _, err := getUpdateHealthHandler(deps)(ctx, req, GetUpdateHealthInput{
		AppId: "app-1", UpdateUUIDs: []string{uuidA},
		From: "2026-07-30T00:00:00Z", To: "2026-07-29T00:00:00Z",
	}); err == nil {
		t.Fatal("expected a refusal on an inverted window")
	}
	if _, _, err := getUpdateHealthHandler(deps)(ctx, req, GetUpdateHealthInput{
		AppId: "app-1", UpdateUUIDs: []string{uuidA}, To: "not-a-timestamp",
	}); err == nil {
		t.Fatal("expected a refusal on a malformed timestamp")
	}
}

// Cohorts are summed and the percentage recomputed: averaging per-update
// percentages would weigh a ten-device update like a ten-thousand-device one.
func TestSumCurrentHealth(t *testing.T) {
	current := sumCurrentHealth(map[string]identityUpdateHealth{
		uuidA: {DevicesOnUpdate: 100, FaultyDevices: 10, UpdateIssues: 7, RuntimeIssues: 4, FailedStillOn: 4},
		uuidB: {DevicesOnUpdate: 10, FaultyDevices: 5, UpdateIssues: 5, RuntimeIssues: 0, FailedStillOn: 5},
	})
	// 96 + 5 successful, 15 faulty.
	if current.DevicesOnUpdate != 110 || current.SuccessfulDevices != 101 || current.FaultyDevices != 15 {
		t.Fatalf("unexpected cohorts: %+v", current)
	}
	if current.UpdateIssues != 12 || current.RuntimeIssues != 4 {
		t.Errorf("fault kinds must add up: %+v", current)
	}
	if current.HealthPercent == nil {
		t.Fatal("a percentage was expected")
	}
	// 101/116, nowhere near the average of the two per-update percentages.
	if *current.HealthPercent < 87 || *current.HealthPercent > 87.1 {
		t.Errorf("expected ~87.07%%, got %v", *current.HealthPercent)
	}

	// No attempt at all leaves the percentage null rather than claiming 0 or 100.
	empty := sumCurrentHealth(map[string]identityUpdateHealth{uuidA: {}})
	if empty.HealthPercent != nil {
		t.Errorf("expected a null percentage, got %v", *empty.HealthPercent)
	}
}

// The one-minute snapshots are resampled to a readable curve, keeping the last
// value of each slice and always the most recent point.
func TestResampleKeepsTheLastPoint(t *testing.T) {
	points := make([]HealthPoint, 1440)
	for i := range points {
		points[i] = HealthPoint{Timestamp: "t", DevicesOnUpdate: uint64(i)}
	}
	kept := resampleHealth(points, 24)
	if len(kept) != 24 {
		t.Fatalf("expected 24 points, got %d", len(kept))
	}
	if kept[len(kept)-1].DevicesOnUpdate != 1439 {
		t.Errorf("the most recent point must survive, got %d", kept[len(kept)-1].DevicesOnUpdate)
	}
	// Fewer points than the ceiling pass through untouched.
	if len(resampleHealth(points[:5], 24)) != 5 {
		t.Error("a short series must not be padded or trimmed")
	}
	if resampleHealth(nil, 24) == nil {
		t.Error("an empty series must stay an empty list, not null")
	}
}

// Its dashboard twin is AnyViewer, so no permission may gate it: even a
// principal denied every permission still sees it, unlike the gated tools.
func TestGetUpdateHealthIsUngated(t *testing.T) {
	deps := healthDeps()
	deps.CanUseSomewhere = func(context.Context, *services.DashboardPrincipal, mittools.Access) bool { return false }

	tools := registeredTools(t, deps, healthPrincipal)
	if !tools["get_update_health"] {
		t.Fatalf("the health tool must survive a principal holding nothing, got %v", tools)
	}
	for _, gated := range []string{"query_audit_logs", "search_devices", "query_logs"} {
		if tools[gated] {
			t.Errorf("%s must be gated, yet it registered for a principal holding nothing", gated)
		}
	}
}
