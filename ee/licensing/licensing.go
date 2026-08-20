// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Package licensing manages the Enterprise Edition license of a deployment
// against the Mercure Technologies license server (api.xprem.dev).
package licensing

import (
	"sync"
	"time"
)

// PlanEnterprise is the only plan code this deployment accepts.
const PlanEnterprise = "enterprise"

// License is the descriptive payload of an attached license, as returned by
// the license server.
type License struct {
	OrgName               string
	PlanCode              string
	SubscriptionStartAt   time.Time
	SubscriptionEndAt     *time.Time // nil means no end date
	SubscriptionRenewalAt *time.Time
}

var (
	mu      sync.RWMutex
	current *License
)

// Activate makes l the active license for this process.
func Activate(l License) {
	mu.Lock()
	current = &l
	mu.Unlock()
}

// Deactivate clears the active license (community edition).
func Deactivate() {
	mu.Lock()
	current = nil
	mu.Unlock()
}

// Current returns the active license, or nil when running as community
// edition.
func Current() *License {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// IsEnterprise reports whether a license is active.
func IsEnterprise() bool {
	return Current() != nil
}
