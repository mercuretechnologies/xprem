// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	"strings"
	"testing"
	"time"

	"expo-open-ota/ee/identity"
	mittools "expo-open-ota/internal/mcptools"
	"expo-open-ota/internal/services"
)

// Without a device registry, every device tool says so instead of failing on a
// nil service.
func TestDeviceToolsWithoutRegistry(t *testing.T) {
	deps := healthDeps()
	ctx := context.Background()
	req := auditRequestFor(healthPrincipal)

	calls := map[string]func() error{
		"search_devices": func() error {
			_, _, err := searchDevicesHandler(deps)(ctx, req, SearchDevicesInput{AppId: "app-1"})
			return err
		},
		"get_device": func() error {
			_, _, err := getDeviceHandler(deps)(ctx, req, GetDeviceInput{AppId: "app-1", EasClientId: uuidA})
			return err
		},
		"count_online_devices": func() error {
			_, _, err := countOnlineDevicesHandler(deps)(ctx, req, CountOnlineDevicesInput{AppId: "app-1"})
			return err
		},
		"get_device_attributes": func() error {
			_, _, err := getDeviceAttributesHandler(deps)(ctx, req, GetDeviceAttributesInput{AppId: "app-1"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil || !strings.Contains(err.Error(), "records nothing about devices") {
				t.Fatalf("expected the no-telemetry answer, got %v", err)
			}
		})
	}
}

// The app gate runs before the registry check, so an account that may not see
// the app learns nothing about the deployment's telemetry setup.
func TestDeviceToolsRefuseUnknownApp(t *testing.T) {
	deps := healthDeps()
	_, _, err := searchDevicesHandler(deps)(context.Background(), auditRequestFor(healthPrincipal), SearchDevicesInput{AppId: "nope"})
	if err == nil || err.Error() != mittools.ErrAppNotFound.Error() {
		t.Fatalf("expected the app-not-found answer, got %v", err)
	}
	if _, _, err := searchDevicesHandler(deps)(context.Background(), auditRequestFor(nil), SearchDevicesInput{AppId: "app-1"}); err == nil {
		t.Fatal("a session without a principal must be refused")
	}
}

// The device cursor is the dashboard's: what one surface hands out, the other
// can read.
func TestDeviceCursorIsShared(t *testing.T) {
	cursor := &identity.DeviceCursor{
		LastSeenAt:  time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
		EASClientID: uuidA,
	}
	encoded := identity.EncodeDeviceCursor(cursor)
	if encoded == "" {
		t.Fatal("a cursor was expected")
	}
	decoded, err := identity.DecodeDeviceCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.EASClientID != uuidA || !decoded.LastSeenAt.Equal(cursor.LastSeenAt) {
		t.Fatalf("cursor did not round-trip: %+v", decoded)
	}
	if identity.EncodeDeviceCursor(nil) != "" {
		t.Error("no cursor must encode to nothing, not to a token pointing nowhere")
	}
	if _, err := identity.DecodeDeviceCursor("not-a-cursor!!"); err == nil {
		t.Error("a tampered cursor must be refused")
	}
}

func TestDeviceFiltersMapOntoTheRegistryQuery(t *testing.T) {
	deps := healthDeps()
	query, err := deps.deviceQuery(context.Background(), "app-1", DeviceFilters{
		Branches:     []string{"main"},
		Platforms:    []string{"ios"},
		CountryCodes: []string{"FR"},
		UpdateIds:    []string{uuidA},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(query.Branches) != 1 || query.Branches[0] != "main" {
		t.Errorf("branches did not map: %+v", query)
	}
	if len(query.CurrentUpdateIDs) != 1 || query.CurrentUpdateIDs[0] != uuidA {
		t.Errorf("updateIds must map onto the current update: %+v", query)
	}
	if len(query.Metadata) != 0 {
		t.Errorf("no attribute filter was asked for: %+v", query.Metadata)
	}
}

func TestGetDeviceAttributesRejectsSearchWithoutKey(t *testing.T) {
	deps := healthDeps()
	deps.Identity = &identity.Service{}
	_, _, err := getDeviceAttributesHandler(deps)(context.Background(), auditRequestFor(healthPrincipal), GetDeviceAttributesInput{
		AppId: "app-1", Search: "pro",
	})
	if err == nil || !strings.Contains(err.Error(), "only applies with a key") {
		t.Fatalf("expected the search/key pairing to be enforced, got %v", err)
	}
}

func TestDeviceToolsAreVisibleAndReadOnly(t *testing.T) {
	deps := healthDeps()
	deps.CanUseSomewhere = func(_ context.Context, _ *services.DashboardPrincipal, access mittools.Access) bool {
		return access.Perm == "identity:read" && access.Fallback == mittools.FallbackAnyMember
	}
	tools := registeredTools(t, deps, healthPrincipal)
	for _, name := range []string{"search_devices", "get_device", "count_online_devices", "get_device_attributes"} {
		if !tools[name] {
			t.Errorf("%s must be registered for a principal holding identity:read", name)
		}
	}
}
