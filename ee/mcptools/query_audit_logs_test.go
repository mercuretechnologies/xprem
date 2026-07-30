// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	"strings"
	"testing"

	"expo-open-ota/ee/audit"
	mittools "expo-open-ota/internal/mcptools"
	"expo-open-ota/internal/services"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

func auditRequestFor(principal *services.DashboardPrincipal) *mcpprot.CallToolRequest {
	extra := map[string]any{}
	if principal != nil {
		extra[services.PrincipalExtraKey] = principal
	}
	return &mcpprot.CallToolRequest{
		Extra: &mcpprot.RequestExtra{TokenInfo: &auth.TokenInfo{Extra: extra}},
	}
}

func TestQueryAuditLogsAdminOnly(t *testing.T) {
	// A nil-repo audit service answers ErrRequiresControlPlane, which is
	// enough to exercise the gates in front of it.
	deps := Deps{Audit: audit.NewAuditService(nil)}
	handler := queryAuditLogsHandler(deps)

	member := &services.DashboardPrincipal{UserId: "user-1"}
	if _, _, err := handler(context.Background(), auditRequestFor(member), QueryAuditLogsInput{}); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("a member must be refused, got %v", err)
	}

	admin := &services.DashboardPrincipal{UserId: "admin-1", IsAdmin: true}
	if _, _, err := handler(context.Background(), auditRequestFor(admin), QueryAuditLogsInput{}); err == nil || !strings.Contains(err.Error(), "control plane") {
		t.Fatalf("expected the stateless answer past the admin gate, got %v", err)
	}
}

func TestAuditToolVisibility(t *testing.T) {
	// The registration table gates the tool on the admin-only access.
	adminOnly := func(_ context.Context, principal *services.DashboardPrincipal, access mittools.Access) bool {
		return principal != nil && principal.IsAdmin && access.Fallback == mittools.FallbackAdminOnly
	}
	deps := Deps{CanUseSomewhere: adminOnly, Audit: audit.NewAuditService(nil)}

	member := registeredTools(t, deps, &services.DashboardPrincipal{UserId: "user-1"})
	if member["query_audit_logs"] {
		t.Errorf("a member session must not see the audit tool, got %v", member)
	}
	admin := registeredTools(t, deps, &services.DashboardPrincipal{UserId: "admin-1", IsAdmin: true})
	if !admin["query_audit_logs"] {
		t.Errorf("an admin session must see the audit tool, got %v", admin)
	}
}

// registeredTools lists a session server's tools through a real client, the
// same path tools/list takes.
func registeredTools(t *testing.T, deps Deps, principal *services.DashboardPrincipal) map[string]bool {
	t.Helper()
	ctx := context.Background()
	server := mcpprot.NewServer(&mcpprot.Implementation{Name: "test", Version: "0"}, nil)
	Configurator(deps)(ctx, principal, server)

	clientTransport, serverTransport := mcpprot.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcpprot.NewClient(&mcpprot.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	return names
}
