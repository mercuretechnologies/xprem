// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"expo-open-ota/config"
	"expo-open-ota/internal/mcptools"
	"expo-open-ota/internal/services"
	"log"
)

// AppLister is the slice of the app store the tool deps need.
type AppLister interface {
	GetApps(ctx context.Context) ([]config.AppDescriptor, error)
}

// MCPToolDeps implements the MIT tool seams against this service: tool
// visibility, per-app authorization, and the whoami permission picture.
func (s *RBACService) MCPToolDeps(apps AppLister) mcptools.Deps {
	return mcptools.Deps{
		CanUseSomewhere: func(ctx context.Context, principal *services.DashboardPrincipal, access mcptools.Access) bool {
			if principal == nil {
				return false
			}
			allowed, err := s.HasPermissionSomewhere(ctx, subjectFor(principal), Permission(access.Perm), fallbackFor(access.Fallback))
			if err != nil {
				log.Printf("mcp tool visibility check failed for user %s: %v", principal.UserId, err)
				return false
			}
			return allowed
		},
		Authorize: func(ctx context.Context, principal *services.DashboardPrincipal, appID string, access mcptools.Access) error {
			if principal == nil {
				return ErrNoAppAccess
			}
			return s.Authorize(ctx, subjectFor(principal), appID, Permission(access.Perm), fallbackFor(access.Fallback))
		},
		DescribePermissions: func(ctx context.Context, principal *services.DashboardPrincipal) (mcptools.AccountPermissions, error) {
			if principal == nil {
				return mcptools.AccountPermissions{}, ErrNoAppAccess
			}
			descriptors, err := apps.GetApps(ctx)
			if err != nil {
				log.Printf("mcp whoami could not list apps: %v", err)
				return mcptools.AccountPermissions{}, err
			}
			allAppIDs := make([]string, len(descriptors))
			for i, descriptor := range descriptors {
				allAppIDs[i] = descriptor.Id
			}
			description, err := s.DescribeAccountPermissions(ctx, subjectFor(principal), allAppIDs)
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
		},
	}
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
