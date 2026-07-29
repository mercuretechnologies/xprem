// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"expo-open-ota/internal/auditlog"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAuditRecorder struct{ events []auditlog.Event }

func (f *fakeAuditRecorder) Record(_ context.Context, event auditlog.Event) {
	f.events = append(f.events, event)
}

func TestRequirePermissionRecordsDenials(t *testing.T) {
	repo := newFakeRepo()
	repo.grants["member-1"] = []AppGrant{{AppID: "app-1", ExtraPermissions: []Permission{PermBranchCreate}}}
	lookup := &fakeUserLookup{users: map[string]store.User{"member-1": {Id: "member-1"}}}
	service := withLookup(licensedService(repo), lookup)
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)
	member := &services.DashboardPrincipal{UserId: "member-1", Email: "member@example.com"}

	// A granted request leaves no event: domain actions are recorded by their
	// own emitters, the middleware only reports refusals.
	require.Equal(t, http.StatusOK,
		performAppRequest(t, RequirePermission(service, PermBranchCreate, FallbackAdminOnly), member).Code)
	require.Empty(t, recorder.events)

	// Granted app, missing permission.
	performAppRequest(t, RequirePermission(service, PermBranchDelete, FallbackAdminOnly), member)
	require.Len(t, recorder.events, 1)
	event := recorder.events[0]
	assert.Equal(t, auditlog.ActionPermissionDenied, event.Action)
	assert.Equal(t, auditlog.OutcomeDenied, event.Outcome)
	assert.Equal(t, "member-1", event.ActorID)
	assert.Equal(t, "member@example.com", event.ActorDisplay)
	assert.Equal(t, "app-1", event.AppID)
	assert.Equal(t, map[string]any{
		"permission": string(PermBranchDelete),
		"method":     http.MethodPost,
		"path":       "/api/apps/app-1/branches",
	}, event.Metadata)

	// No grant on the app at all: same event, named reason.
	repo.grants["member-1"] = nil
	performAppRequest(t, RequirePermission(service, PermBranchCreate, FallbackAdminOnly), member)
	require.Len(t, recorder.events, 2)
	assert.Equal(t, "no_app_grant", recorder.events[1].Metadata["reason"])
}

func TestRequirePermissionCommunityFallbackRecordsNothing(t *testing.T) {
	// Without a license the refusal is the community admin-only gate, not an
	// enterprise denial.
	repo := newFakeRepo()
	lookup := &fakeUserLookup{users: map[string]store.User{"member-1": {Id: "member-1"}}}
	service := withLookup(unlicensedService(repo), lookup)
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)

	performAppRequest(t, RequirePermission(service, PermBranchCreate, FallbackAdminOnly),
		&services.DashboardPrincipal{UserId: "member-1"})
	require.Empty(t, recorder.events)
}

func TestRequireAppVisibleRecordsDenials(t *testing.T) {
	repo := newFakeRepo()
	lookup := &fakeUserLookup{users: map[string]store.User{"member-1": {Id: "member-1"}}}
	service := withLookup(licensedService(repo), lookup)
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)

	performAppRequest(t, RequireAppVisible(service),
		&services.DashboardPrincipal{UserId: "member-1", Email: "member@example.com"})

	require.Len(t, recorder.events, 1)
	event := recorder.events[0]
	assert.Equal(t, auditlog.ActionPermissionDenied, event.Action)
	assert.Equal(t, "no_app_grant", event.Metadata["reason"])
	assert.Equal(t, "app-1", event.AppID)
	// No permission key: visibility is not one permission, it is the grant
	// itself.
	assert.NotContains(t, event.Metadata, "permission")
}
