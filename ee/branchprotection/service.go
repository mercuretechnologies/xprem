// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Package branchprotection guards protected branches against deletion.
package branchprotection

import (
	"context"
	"errors"
	"xprem/ee/licensing"
	"xprem/internal/auditlog"
	"xprem/internal/services"
)

// Repository persists the branch protection flag.
type Repository interface {
	SetBranchProtection(ctx context.Context, appID string, branchName string, protected bool) error
}

var (
	ErrRequiresControlPlane = errors.New("branch protection is stored in the database: this deployment runs in stateless mode, which is community edition only")
	ErrRequiresValidLicense = errors.New("branch protection requires an active enterprise license")
	ErrBranchNotFound       = errors.New("branch not found")
)

// Service owns the branch protection toggle. A nil repository (stateless mode)
// makes every call return ErrRequiresControlPlane.
type Service struct {
	repo Repository
	// licenseValid reports whether the enterprise license is active.
	licenseValid func() bool
	// onAuditEvent is the audit emission seam; nil means changes leave no events.
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

// recordEvent reports the change; the actor is the dashboard principal on the
// request context.
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
