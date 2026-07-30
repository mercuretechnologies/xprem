// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"expo-open-ota/internal/mcptools"
	"expo-open-ota/internal/services"
	"log"
)

// The MCP-facing decision functions: each one matches a mcptools.Deps field,
// speaks the MIT tool vocabulary, and fails closed. Wire assigns them as
// method values; this package never builds the Deps itself.

// MCPCanUseSomewhere decides tool visibility at session creation.
func (s *RBACService) MCPCanUseSomewhere(ctx context.Context, principal *services.DashboardPrincipal, access mcptools.Access) bool {
	if principal == nil {
		return false
	}
	allowed, err := s.HasPermissionSomewhere(ctx, subjectFor(principal), Permission(access.Perm), fallbackFor(access.Fallback))
	if err != nil {
		log.Printf("mcp tool visibility check failed for user %s: %v", principal.UserId, err)
		return false
	}
	return allowed
}

// MCPAuthorizeTool gates one tool execution on one app.
func (s *RBACService) MCPAuthorizeTool(ctx context.Context, principal *services.DashboardPrincipal, appID string, access mcptools.Access) error {
	if principal == nil {
		return ErrNoAppAccess
	}
	return s.Authorize(ctx, subjectFor(principal), appID, Permission(access.Perm), fallbackFor(access.Fallback))
}

// MCPDescribePermissions answers whoami over the given apps.
func (s *RBACService) MCPDescribePermissions(ctx context.Context, principal *services.DashboardPrincipal, appIDs []string) (mcptools.AccountPermissions, error) {
	if principal == nil {
		return mcptools.AccountPermissions{}, ErrNoAppAccess
	}
	description, err := s.DescribeAccountPermissions(ctx, subjectFor(principal), appIDs)
	if err != nil {
		log.Printf("mcp whoami could not describe the permissions of user %s: %v", principal.UserId, err)
		return mcptools.AccountPermissions{}, err
	}
	result := mcptools.AccountPermissions{
		Role:          description.Role,
		RolesEnforced: description.RolesEnforced,
	}
	for _, app := range description.Apps {
		result.Apps = append(result.Apps, mcptools.AppPermissions{
			AppID:   app.AppID,
			Granted: permissionNames(app.Granted),
			Denied:  permissionNames(app.Denied),
		})
	}
	return result, nil
}

func subjectFor(principal *services.DashboardPrincipal) Subject {
	return Subject{UserID: principal.UserId, IsAdmin: principal.IsAdmin}
}

// fallbackFor maps the MIT tool vocabulary onto this package's.
func fallbackFor(fallback mcptools.Fallback) Fallback {
	if fallback == mcptools.FallbackAnyMember {
		return FallbackAnyMember
	}
	return FallbackAdminOnly
}

func permissionNames(permissions []Permission) []string {
	names := make([]string, len(permissions))
	for i, permission := range permissions {
		names[i] = string(permission)
	}
	return names
}

// MustValidateMCPTools checks that every permission the MIT tool tables gate
// on exists in this catalog, and panics at boot otherwise. The tool packages
// are MIT and hold those permissions as plain strings; this is what keeps a
// typo from silently turning a tool admin-only.
func MustValidateMCPTools(declared ...[]string) {
	for _, perms := range declared {
		for _, perm := range perms {
			if !IsValidPermission(perm) {
				panic("mcp tools: unknown permission " + perm)
			}
		}
	}
}
