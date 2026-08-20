// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func acmeLicense() License {
	renewal := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	return License{
		OrgName:               "Acme Corp",
		PlanCode:              "enterprise",
		SubscriptionStartAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		SubscriptionRenewalAt: &renewal,
	}
}

func TestActivateDeactivateCurrent(t *testing.T) {
	t.Cleanup(Deactivate)

	Deactivate()
	assert.Nil(t, Current())
	assert.False(t, IsEnterprise())

	Activate(acmeLicense())
	require.NotNil(t, Current())
	assert.Equal(t, "Acme Corp", Current().OrgName)
	assert.Equal(t, "enterprise", Current().PlanCode)
	assert.True(t, IsEnterprise())

	Deactivate()
	assert.Nil(t, Current())
	assert.False(t, IsEnterprise())
}

func TestLicenseStatusGraceWindow(t *testing.T) {
	license := acmeLicense()

	healthy := LicenseStatus{HasKey: true, License: &license}
	assert.True(t, healthy.Valid())
	assert.False(t, healthy.Suspended())
	assert.Nil(t, healthy.GraceDeadline())

	failedRecently := time.Now().Add(-time.Hour)
	inGrace := LicenseStatus{HasKey: true, License: &license, ValidationFailedAt: &failedRecently}
	assert.True(t, inGrace.Valid(), "a license failing for an hour is still in its grace window")
	assert.False(t, inGrace.Suspended())
	require.NotNil(t, inGrace.GraceDeadline())
	assert.WithinDuration(t, failedRecently.Add(GracePeriod), *inGrace.GraceDeadline(), time.Second)

	failedLongAgo := time.Now().Add(-GracePeriod - time.Hour)
	suspended := LicenseStatus{HasKey: true, License: &license, ValidationFailedAt: &failedLongAgo}
	assert.False(t, suspended.Valid())
	assert.True(t, suspended.Suspended())

	assert.False(t, LicenseStatus{}.Valid(), "no key means community edition")
}
