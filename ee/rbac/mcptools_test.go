// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"expo-open-ota/internal/mcptools"
	"expo-open-ota/internal/services"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPDecisionFunctions(t *testing.T) {
	ctx := context.Background()
	service := NewRBACService(newFakeRepo(), nil)
	service.licenseValid = func() bool { return false }

	member := &services.DashboardPrincipal{UserId: "user-1"}
	admin := &services.DashboardPrincipal{UserId: "admin-1", IsAdmin: true}

	// Fail closed without a principal, on every decision.
	require.False(t, service.MCPCanUseSomewhere(ctx, nil, mcptools.Access{Perm: "observe:read", Fallback: mcptools.FallbackAnyMember}))
	require.Error(t, service.MCPAuthorizeTool(ctx, nil, "app-1", mcptools.Access{Perm: "observe:read", Fallback: mcptools.FallbackAnyMember}))
	_, err := service.MCPDescribePermissions(ctx, nil, []string{"app-1"})
	require.Error(t, err)

	// The fallback vocabulary maps: without a license, AnyMember opens to
	// members, AdminOnly does not.
	require.True(t, service.MCPCanUseSomewhere(ctx, member, mcptools.Access{Perm: "observe:read", Fallback: mcptools.FallbackAnyMember}))
	require.False(t, service.MCPCanUseSomewhere(ctx, member, mcptools.Access{Perm: "update:publish", Fallback: mcptools.FallbackAdminOnly}))

	// The whoami picture: one entry per app, community translation.
	description, err := service.MCPDescribePermissions(ctx, member, []string{"app-1"})
	require.NoError(t, err)
	require.False(t, description.RolesEnforced)
	require.Equal(t, "member", description.Role)
	require.Len(t, description.Apps, 1)
	require.Equal(t, []string{string(PermIdentityRead), string(PermObserveRead)}, description.Apps[0].Granted)
	require.Len(t, description.Apps[0].Denied, len(AllPermissions)-2)

	adminDescription, err := service.MCPDescribePermissions(ctx, admin, []string{"app-1"})
	require.NoError(t, err)
	require.Equal(t, "admin", adminDescription.Role)
	require.Len(t, adminDescription.Apps[0].Granted, len(AllPermissions))
}
