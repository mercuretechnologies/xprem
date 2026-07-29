// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"testing"
	"xprem/internal/auditlog"
	"xprem/internal/services"
	"xprem/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adminCtx carries the admin principal the way the admin-gated routes do.
func adminCtx() context.Context {
	return services.WithPrincipal(context.Background(),
		&services.DashboardPrincipal{UserId: "admin-1", Email: "admin@example.com", IsAdmin: true})
}

func TestRoleLifecycleEmitsAuditEvents(t *testing.T) {
	repo := newFakeRepo()
	service := licensedService(repo)
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)
	ctx := adminCtx()

	role, err := service.CreateRole(ctx, "Release manager", []Permission{PermChannelRolloutManage})
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)
	created := recorder.events[0]
	assert.Equal(t, auditlog.ActionRoleCreated, created.Action)
	assert.Equal(t, "admin-1", created.ActorID)
	assert.Equal(t, "admin@example.com", created.ActorDisplay)
	assert.Equal(t, role.ID, created.TargetID)
	assert.Equal(t, "Release manager", created.TargetDisplay)
	assert.Equal(t, map[string]any{"permissions": []string{string(PermChannelRolloutManage)}}, created.Metadata)

	require.NoError(t, service.UpdateRole(ctx, role.ID, "Release manager v2", []Permission{PermBranchCreate}))
	require.Len(t, recorder.events, 2)
	assert.Equal(t, auditlog.ActionRoleUpdated, recorder.events[1].Action)
	assert.Equal(t, "Release manager v2", recorder.events[1].TargetDisplay)

	// The deletion entry still names the role: read before the row went away.
	require.NoError(t, service.DeleteRole(ctx, role.ID))
	require.Len(t, recorder.events, 3)
	assert.Equal(t, auditlog.ActionRoleDeleted, recorder.events[2].Action)
	assert.Equal(t, "Release manager v2", recorder.events[2].TargetDisplay)
}

func TestSetUserGrantsEmitsAuditEvent(t *testing.T) {
	repo := newFakeRepo()
	lookup := &fakeUserLookup{users: map[string]store.User{
		"member-1": {Id: "member-1", Email: "member@example.com"},
	}}
	service := withLookup(licensedService(repo), lookup)
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)

	roleID := "role-1"
	require.NoError(t, service.SetUserGrants(adminCtx(), "member-1", []GrantInput{
		{AppID: "app-1", RoleID: &roleID},
		{AppID: "app-2", ExtraPermissions: []Permission{PermBranchCreate}},
	}))

	require.Len(t, recorder.events, 1)
	event := recorder.events[0]
	assert.Equal(t, auditlog.ActionUserGrantsUpdated, event.Action)
	assert.Equal(t, "member-1", event.TargetID)
	assert.Equal(t, "member@example.com", event.TargetDisplay)
	assert.Equal(t, map[string]any{"grants": []map[string]any{
		{"app_id": "app-1", "role_id": "role-1"},
		{"app_id": "app-2", "extra_permissions": []string{string(PermBranchCreate)}},
	}}, event.Metadata)
}

func TestUnlicensedRoleMutationsEmitNothing(t *testing.T) {
	service := unlicensedService(newFakeRepo())
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)

	_, err := service.CreateRole(adminCtx(), "Blocked", nil)
	require.ErrorIs(t, err, ErrRequiresValidLicense)
	require.Empty(t, recorder.events)
}
