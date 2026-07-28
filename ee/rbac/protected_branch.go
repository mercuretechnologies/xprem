// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"errors"
	"expo-open-ota/internal/handlers"
	"fmt"
	"net/http"
)

// BranchProtection is the one read the guard below needs.
// apikeyrestrictions.ApiKeyRestrictionService satisfies it, and answers false
// whenever protection is not enforced (no control plane, no valid license), so
// the guard disappears on its own outside enterprise deployments.
type BranchProtection interface {
	IsBranchProtected(ctx context.Context, appID string, branchName string) (bool, error)
}

// NewProtectedBranchGuard builds the check the dashboard publish handler runs
// on the branch it is about to write to: on a PROTECTED branch the account
// needs perm on top of whatever its route already asked for.
//
// It is a guard the handler calls rather than a middleware on the route because
// what it narrows is not the endpoint but the publish: the route's permission
// says who may reach it at all, this says which branches their publish may
// land on.
//
// The returned function is the seam the composition root plugs into
// dashboard.UpdateHandler.SetProtectedBranchGuard; unplugged, every branch
// stays open to whoever passed the route's permission. Refusals wrap
// handlers.ErrAccessDenied so the community handler maps them to 403
// without importing anything from here, and are recorded as permission.denied
// like every other refusal.
func NewProtectedBranchGuard(
	service *RBACService,
	protection BranchProtection,
	perm Permission,
) func(r *http.Request, appID string, branchName string) error {
	return func(r *http.Request, appID string, branchName string) error {
		if service == nil || protection == nil || branchName == "" {
			return nil
		}
		// Protection first: on the common path (an unprotected branch) this
		// costs one indexed read and no users-table lookup at all.
		protected, err := protection.IsBranchProtected(r.Context(), appID, branchName)
		if err != nil {
			return fmt.Errorf("failed to read branch protection: %w", err)
		}
		if !protected {
			return nil
		}
		subject, err := service.subjectFromContext(r.Context())
		if err != nil {
			return fmt.Errorf("%w: %s", handlers.ErrAccessDenied, err)
		}
		// FallbackAdminOnly matches the routes this guards. It is only ever
		// reached with protection enforced, which means with a valid license,
		// which is also when roles are enforced: the fallback is unreachable
		// here, and admin-only is the answer that stays safe if it ever is not.
		authErr := service.Authorize(r.Context(), subject, appID, perm, FallbackAdminOnly)
		if authErr == nil {
			return nil
		}
		service.recordDenied(r, subject, appID, authErr, map[string]any{
			"permission": string(perm),
			"branch":     branchName,
			"reason":     "protected_branch",
		})
		deniedErr := (*ErrPermissionDenied)(nil)
		if errors.As(authErr, &deniedErr) {
			return fmt.Errorf("%w: %s is a protected branch, and %s", handlers.ErrAccessDenied, branchName, deniedErr.Error())
		}
		return fmt.Errorf("%w: %s is a protected branch", handlers.ErrAccessDenied, branchName)
	}
}
