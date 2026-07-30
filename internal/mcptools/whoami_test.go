package mcptools

import (
	"context"
	"errors"
	"expo-open-ota/config"
	"expo-open-ota/internal/services"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

func callToolRequestFor(principal *services.DashboardPrincipal) *mcpprot.CallToolRequest {
	extra := map[string]any{}
	if principal != nil {
		extra[services.PrincipalExtraKey] = principal
	}
	return &mcpprot.CallToolRequest{
		Extra: &mcpprot.RequestExtra{
			TokenInfo: &auth.TokenInfo{UserID: "user-1", Extra: extra},
		},
	}
}

type fakeAppLister struct{}

func (fakeAppLister) GetApps(context.Context) ([]config.AppDescriptor, error) {
	return []config.AppDescriptor{{Id: "app-1"}}, nil
}

func testDeps() Deps {
	return Deps{
		Apps: fakeAppLister{},
		VisibleApps: func(_ context.Context, _ *services.DashboardPrincipal) (bool, map[string]bool, error) {
			return false, nil, nil
		},
		CanUseSomewhere: func(_ context.Context, principal *services.DashboardPrincipal, _ Access) bool {
			return principal != nil && principal.IsAdmin
		},
		Authorize: func(_ context.Context, _ *services.DashboardPrincipal, _ string, _ Access) error {
			return errors.New("denied")
		},
		DescribePermissions: func(_ context.Context, principal *services.DashboardPrincipal, appIDs []string) (AccountPermissions, error) {
			apps := make([]AppPermissions, len(appIDs))
			for i, appID := range appIDs {
				apps[i] = AppPermissions{AppID: appID, Granted: []string{"observe:read"}, Denied: []string{"app:delete"}}
			}
			return AccountPermissions{Role: "member", Apps: apps}, nil
		},
	}
}

// registeredTools builds a session server through Configurator and lists its
// tools through a real in-memory client, the same path tools/list takes.
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

func TestWhoami(t *testing.T) {
	principal := &services.DashboardPrincipal{UserId: "user-1", Email: "a@b.c"}
	// Through the real handler and the real Extra-key contract with the OAuth
	// verifier: a drift on either side of the key fails here.
	_, output, err := whoamiHandler(testDeps())(context.Background(), callToolRequestFor(principal), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if output.UserId != "user-1" || output.Email != "a@b.c" {
		t.Errorf("unexpected identity: %+v", output)
	}
	permissions := output.Permissions
	if permissions.Role != "member" || len(permissions.Apps) != 1 || len(permissions.Apps[0].Granted) != 1 {
		t.Errorf("unexpected description: %+v", permissions)
	}
}

func TestWhoamiWithoutPrincipal(t *testing.T) {
	for name, req := range map[string]*mcpprot.CallToolRequest{
		"no token info": {Extra: &mcpprot.RequestExtra{}},
		"no extra":      {},
		"no principal":  callToolRequestFor(nil),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := whoamiHandler(testDeps())(context.Background(), req, struct{}{}); err == nil {
				t.Fatal("expected an error without a principal")
			}
		})
	}
}

func TestConfiguratorFiltersGatedTools(t *testing.T) {
	deps := testDeps()
	gated := Access{Perm: "update:publish", Fallback: FallbackAdminOnly}
	registrations = append(registrations, struct {
		register func(*mcpprot.Server, Deps)
		access   *Access
	}{
		register: func(server *mcpprot.Server, deps Deps) {
			mcpprot.AddTool(server, &mcpprot.Tool{Name: "gated_tool", Description: "test"},
				func(_ context.Context, _ *mcpprot.CallToolRequest, _ struct{}) (*mcpprot.CallToolResult, struct{}, error) {
					return nil, struct{}{}, nil
				})
		},
		access: &gated,
	})
	defer func() { registrations = registrations[:len(registrations)-1] }()

	admin := &services.DashboardPrincipal{UserId: "admin-1", IsAdmin: true}
	member := &services.DashboardPrincipal{UserId: "user-1"}

	adminTools := registeredTools(t, deps, admin)
	if !adminTools["whoami"] || !adminTools["gated_tool"] {
		t.Errorf("admin must see every tool, got %v", adminTools)
	}
	memberTools := registeredTools(t, deps, member)
	if !memberTools["whoami"] || memberTools["gated_tool"] {
		t.Errorf("member must not see the gated tool, got %v", memberTools)
	}
}
