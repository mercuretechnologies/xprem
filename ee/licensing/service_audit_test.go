// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"context"
	"testing"
	"time"
	"xprem/internal/auditlog"
	"xprem/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The recorder mirrors audit.AuditService.Record's license gate (an event
// emitted after the gate closed is dropped) with a LOCAL func rather than the
// real AuditService: this package must not import ee/audit, which imports
// ee/licensing back (the gate lives in EE code by design), so pulling the real
// service in here would be an import cycle. The behavior under test is
// licensing's own ordering (emit BEFORE Deactivate), and the local gate
// reproduces exactly what would drop a mis-ordered event.
func gatedRecorder(recorded *[]auditlog.Event) auditlog.RecordFunc {
	return func(_ context.Context, event auditlog.Event) {
		if IsEnterprise() {
			*recorded = append(*recorded, event)
		}
	}
}

func TestActivateAndRemoveEmitAuditEvents(t *testing.T) {
	priv := setupTestKeypair(t)
	service := NewLicenseService(&fakeLicenseRepo{})
	var recorded []auditlog.Event
	service.SetOnAuditEvent(gatedRecorder(&recorded))
	ctx := services.WithPrincipal(context.Background(),
		&services.DashboardPrincipal{UserId: "admin-1", Email: "admin@example.com"})

	expiry := time.Now().Add(365 * 24 * time.Hour).UTC()
	_, err := service.Activate(ctx, signTestKey(t, priv, &expiry))
	require.NoError(t, err)

	// The activation itself is recorded through the gate it just opened.
	require.Len(t, recorded, 1)
	activated := recorded[0]
	assert.Equal(t, auditlog.ActionLicenseActivated, activated.Action)
	assert.Equal(t, "admin-1", activated.ActorID)
	assert.Equal(t, "admin@example.com", activated.ActorDisplay)
	assert.NotEmpty(t, activated.Metadata["license_id"])
	assert.NotEmpty(t, activated.Metadata["expires_at"])

	require.NoError(t, service.Remove(ctx))

	// Emitted before Deactivate: the removal is the last entry the license
	// gate lets through. A regression emitting after would drop it here.
	require.Len(t, recorded, 2)
	assert.Equal(t, auditlog.ActionLicenseRemoved, recorded[1].Action)
	assert.False(t, IsEnterprise())
}
