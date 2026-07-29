// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"context"
	"testing"
	"xprem/internal/auditlog"
	"xprem/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAuditRecorder struct{ events []auditlog.Event }

func (f *fakeAuditRecorder) Record(_ context.Context, event auditlog.Event) {
	f.events = append(f.events, event)
}

func TestRestrictionChangesEmitAuditEvents(t *testing.T) {
	service := serviceWith(&fakeRestrictionRepo{}, true)
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)
	ctx := services.WithPrincipal(context.Background(),
		&services.DashboardPrincipal{UserId: "admin-1", Email: "admin@example.com"})

	// Unmasked input on purpose: the entry must carry the normalized form the
	// database persists and enforces, not what the admin typed.
	require.NoError(t, service.SetRestrictions(ctx, "app-1", 42, false, []string{"10.0.0.5/8"}))
	require.Len(t, recorder.events, 1)
	restricted := recorder.events[0]
	assert.Equal(t, auditlog.ActionAPIKeyRestrictionsUpdated, restricted.Action)
	assert.Equal(t, "admin-1", restricted.ActorID)
	assert.Equal(t, "42", restricted.TargetID)
	// The entry names the key, like api_key.created/revoked.
	assert.Equal(t, "ci-production", restricted.TargetDisplay)
	assert.Equal(t, "app-1", restricted.AppID)
	assert.Equal(t, map[string]any{
		"can_access_protected_branches": false,
		"allowed_cidrs":                 []string{"10.0.0.0/8"},
	}, restricted.Metadata)

	require.NoError(t, service.SetBranchProtection(ctx, "app-1", "main", true))
	require.Len(t, recorder.events, 2)
	protected := recorder.events[1]
	assert.Equal(t, auditlog.ActionBranchProtectionUpdated, protected.Action)
	assert.Equal(t, "main", protected.TargetID)
	assert.Equal(t, map[string]any{"protected": true}, protected.Metadata)
}

func TestUnlicensedRestrictionChangesEmitNothing(t *testing.T) {
	service := serviceWith(&fakeRestrictionRepo{}, false)
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)

	err := service.SetBranchProtection(context.Background(), "app-1", "main", true)
	require.ErrorIs(t, err, ErrRequiresValidLicense)
	require.Empty(t, recorder.events)
}
