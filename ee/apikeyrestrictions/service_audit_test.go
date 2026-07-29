// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"context"
	"expo-open-ota/internal/auditlog"
	"expo-open-ota/internal/services"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAuditRecorder struct{ events []auditlog.Event }

func (f *fakeAuditRecorder) Record(_ context.Context, event auditlog.Event) {
	f.events = append(f.events, event)
}

func TestAccessChangesEmitAuditEvents(t *testing.T) {
	service := serviceWith(&fakeAccessRepo{}, true)
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)
	ctx := services.WithPrincipal(context.Background(),
		&services.DashboardPrincipal{UserId: "admin-1", Email: "admin@example.com"})

	// Unmasked input on purpose; the event must carry the normalized form.
	require.NoError(t, service.SetAccess(ctx, "app-1", 42,
		[]BranchRule{{Pattern: "pr-*", Actions: []Action{ActionPublish, ActionRead}}},
		[]string{"10.0.0.5/8"}))
	require.Len(t, recorder.events, 1)
	restricted := recorder.events[0]
	assert.Equal(t, auditlog.ActionAPIKeyRestrictionsUpdated, restricted.Action)
	assert.Equal(t, "admin-1", restricted.ActorID)
	assert.Equal(t, "42", restricted.TargetID)
	// The entry names the key, like api_key.created/revoked.
	assert.Equal(t, "ci-production", restricted.TargetDisplay)
	assert.Equal(t, "app-1", restricted.AppID)
	// Rules land in the form the dashboard shows, and in catalog order.
	assert.Equal(t, map[string]any{
		"branch_rules":  []string{"pr-*:read+publish"},
		"allowed_cidrs": []string{"10.0.0.0/8"},
	}, restricted.Metadata)

}

func TestUnlicensedAccessChangesEmitNothing(t *testing.T) {
	service := serviceWith(&fakeAccessRepo{}, false)
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)

	err := service.SetAccess(context.Background(), "app-1", 42, nil, nil)
	require.ErrorIs(t, err, ErrRequiresValidLicense)
	require.Empty(t, recorder.events)
}
