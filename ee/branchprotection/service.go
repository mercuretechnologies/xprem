// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Package branchprotection is the deletion lock on a branch, and only that.
//
// It used to be half of the API key access model: a branch was marked, a key
// was allowed onto marked branches or not. That half moved to per-key branch
// rules (ee/apikeyrestrictions), which say which branches a key reaches and
// what it does there. What was left of the flag is the guard that refuses to
// delete a protected branch, admins included, and it lives here rather than
// next to the key rules because it has nothing to do with what a key may
// publish.
//
// The refusal itself is in the DELETE statement (internal/store, queries.sql),
// so a concurrent protect cannot race it. This package only owns turning the
// flag on and off.
package branchprotection

import (
	"context"
	"errors"
	"expo-open-ota/ee/licensing"
	"expo-open-ota/internal/auditlog"
	"expo-open-ota/internal/services"
)

// Repository persists the flag. Reading it is the store's business, on the
// deletion path, so nothing is exposed here for that.
type Repository interface {
	SetBranchProtection(ctx context.Context, appID string, branchName string, protected bool) error
}

var (
	ErrRequiresControlPlane = errors.New("branch protection is stored in the database: this deployment runs in stateless mode, which is community edition only")
	ErrRequiresValidLicense = errors.New("branch protection requires an active enterprise license")
	ErrBranchNotFound       = errors.New("branch not found")
)

// Service owns the toggle. A nil repository (stateless mode) makes every call
// answer ErrRequiresControlPlane.
type Service struct {
	repo Repository
	// licenseValid is the live licensing state; a field so same-package tests
	// can pin it without minting signed keys.
	licenseValid func() bool
	// onAuditEvent is the audit emission seam; nil means changes leave no
	// events.
	onAuditEvent auditlog.RecordFunc
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo, licenseValid: licensing.IsEnterprise}
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *Service) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

func (s *Service) SetBranchProtection(ctx context.Context, appID string, branchName string, protected bool) error {
	if s.repo == nil {
		return ErrRequiresControlPlane
	}
	if !s.licenseValid() {
		return ErrRequiresValidLicense
	}
	if err := s.repo.SetBranchProtection(ctx, appID, branchName, protected); err != nil {
		return err
	}
	s.recordEvent(ctx, appID, branchName, protected)
	return nil
}

// recordEvent reports the change, actor = the dashboard principal on the
// request context (the route is a permission-gated dashboard mutation).
func (s *Service) recordEvent(ctx context.Context, appID string, branchName string, protected bool) {
	if s.onAuditEvent == nil {
		return
	}
	actorID, actorDisplay := "", ""
	if principal := services.PrincipalFromContext(ctx); principal != nil {
		actorID, actorDisplay = principal.UserId, principal.Email
		if actorDisplay == "" {
			actorDisplay = principal.UserId
		}
	}
	s.onAuditEvent(ctx, auditlog.Event{
		ActorType:     auditlog.ActorUser,
		ActorID:       actorID,
		ActorDisplay:  actorDisplay,
		Action:        auditlog.ActionBranchProtectionUpdated,
		TargetType:    "branch",
		TargetID:      branchName,
		TargetDisplay: branchName,
		AppID:         appID,
		Outcome:       auditlog.OutcomeSuccess,
		Metadata:      map[string]any{"protected": protected},
	})
}
