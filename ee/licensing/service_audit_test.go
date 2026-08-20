// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"context"
	"net/http"
	"testing"
	"time"
	"xprem/internal/auditlog"
	"xprem/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatedRecorder mirrors the audit service's license gate locally (importing
// ee/audit here would be an import cycle).
func gatedRecorder(recorded *[]auditlog.Event) auditlog.RecordFunc {
	return func(_ context.Context, event auditlog.Event) {
		if IsEnterprise() {
			*recorded = append(*recorded, event)
		}
	}
}

func TestAttachAndRemoveEmitAuditEvents(t *testing.T) {
	fake := &fakeLicenseServer{
		check: checkAnswersValidAcme,
		attach: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusOK, `{"isActive": true, "activationSecret": "secret-42", "licenseInformations": `+acmeInformationsJSON+`}`)
		},
	}
	service := newTestService(t, &fakeLicenseRepo{}, fake.client(t))
	var recorded []auditlog.Event
	service.SetOnAuditEvent(gatedRecorder(&recorded))
	ctx := services.WithPrincipal(context.Background(),
		&services.DashboardPrincipal{UserId: "admin-1", Email: "admin@example.com"})

	_, err := service.Attach(ctx, "XPREM-KEY")
	require.NoError(t, err)

	// The activation itself is recorded through the gate it just opened.
	require.Len(t, recorded, 1)
	activated := recorded[0]
	assert.Equal(t, auditlog.ActionLicenseActivated, activated.Action)
	assert.Equal(t, "admin-1", activated.ActorID)
	assert.Equal(t, "admin@example.com", activated.ActorDisplay)
	assert.Equal(t, "Acme Corp", activated.Metadata["org"])
	assert.Equal(t, "enterprise", activated.Metadata["plan_code"])

	require.NoError(t, service.Remove(ctx))

	// Emitted before Deactivate, so the gated recorder still sees it.
	require.Len(t, recorded, 2)
	assert.Equal(t, auditlog.ActionLicenseRemoved, recorded[1].Action)
	assert.False(t, IsEnterprise())
}

func TestSuspensionEmitsSystemAuditEvent(t *testing.T) {
	repo := repoWithAcme()
	anchor := time.Now().Add(-GracePeriod - time.Hour).UTC()
	repo.stored.ValidationFailedAt = &anchor

	fake := &fakeLicenseServer{
		validate: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusBadRequest, `{"valid": false, "errorCode": "SUBSCRIPTION_INACTIVE"}`)
		},
	}
	service := newTestService(t, repo, fake.client(t))
	var recorded []auditlog.Event
	service.SetOnAuditEvent(gatedRecorder(&recorded))
	Activate(repo.stored.License)

	service.ValidateNow(context.Background())

	// Emitted before Deactivate, so the gated recorder still sees it.
	require.Len(t, recorded, 1)
	suspended := recorded[0]
	assert.Equal(t, auditlog.ActionLicenseSuspended, suspended.Action)
	assert.Equal(t, auditlog.ActorSystem, suspended.ActorType)
	assert.Equal(t, CodeSubscriptionInactive, suspended.Metadata["error_code"])
	assert.False(t, IsEnterprise())
}

func TestSyncEmitsSuspensionEventBeforeDroppingTheLicense(t *testing.T) {
	repo := repoWithAcme()
	anchor := time.Now().Add(-GracePeriod - time.Hour).UTC()
	repo.stored.ValidationFailedAt = &anchor
	repo.stored.ValidationErrorCode = CodeSubscriptionInactive
	service := newTestService(t, repo, NewClient("http://127.0.0.1:1"))
	var recorded []auditlog.Event
	service.SetOnAuditEvent(gatedRecorder(&recorded))
	Activate(repo.stored.License)

	service.syncFromStore(context.Background())

	// Emitted before applyStatus, so the gated recorder still sees it.
	require.Len(t, recorded, 1)
	assert.Equal(t, auditlog.ActionLicenseSuspended, recorded[0].Action)
	assert.Equal(t, CodeSubscriptionInactive, recorded[0].Metadata["error_code"])
	assert.False(t, IsEnterprise())

	service.syncFromStore(context.Background())
	assert.Len(t, recorded, 1, "an already-deactivated process must not re-emit the suspension")
}
