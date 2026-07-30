package mcptools

import (
	"context"
	"expo-open-ota/config"
	"expo-open-ota/internal/services"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Fallback is what gates a tool when roles are not enforced; the MIT twin of
// the rbac fallback the composition root maps it to.
type Fallback int

const (
	FallbackAdminOnly Fallback = iota + 1
	FallbackAnyMember
)

// Access declares who may see and call a gated tool: the rbac permission once
// roles are enforced, the fallback until then.
type Access struct {
	Perm     string
	Fallback Fallback
}

// AppPermissions is the whole authorization truth of one account on one app:
// every catalog permission, held or not.
type AppPermissions struct {
	AppID   string   `json:"appId"`
	Granted []string `json:"granted"`
	Denied  []string `json:"denied"`
}

// AccountPermissions is the account's full permission picture, one entry per
// app it can see; the MIT twin of the rbac description wire maps onto it.
type AccountPermissions struct {
	Role          string           `json:"role"`
	RolesEnforced bool             `json:"rolesEnforced"`
	Apps          []AppPermissions `json:"apps"`
}

// AppLister is the slice of the app store the tools need.
type AppLister interface {
	GetApps(ctx context.Context) ([]config.AppDescriptor, error)
}

// Deps carries what tools need from the composition root: MIT data access
// injected as-is, and decision functions whose implementations live in ee.
// Every field is a plain method value; wire assembles the struct without a
// line of logic, and tools compose data with decisions.
type Deps struct {
	// Apps is the app store; visibility is decided by VisibleApps, never here.
	Apps AppLister
	// VisibleApps is the visibility decision: restricted=false means every
	// app is visible to the principal.
	VisibleApps func(ctx context.Context, principal *services.DashboardPrincipal) (restricted bool, visible map[string]bool, err error)
	// CanUseSomewhere decides tool visibility at session creation: whether
	// the principal may use an access-gated tool on at least one app.
	CanUseSomewhere func(ctx context.Context, principal *services.DashboardPrincipal, access Access) bool
	// Authorize gates one tool execution on one app, with the same semantics
	// as a route guard.
	Authorize func(ctx context.Context, principal *services.DashboardPrincipal, appID string, access Access) error
	// DescribePermissions answers whoami: the account's full permission
	// picture over the given apps, granted and denied.
	DescribePermissions func(ctx context.Context, principal *services.DashboardPrincipal, appIDs []string) (AccountPermissions, error)
}

// registrations is the tool table: one line per tool, the MCP twin of a
// routes_*.go file. access nil means any authenticated account.
var registrations = []struct {
	register func(*mcpprot.Server, Deps)
	access   *Access
}{
	{register: registerWhoami},
	{register: registerGetApps},
}

// Configurator returns what NewMCPService needs: a function that populates
// one session's server with the tools its principal may use. Gated tools are
// simply absent from a session that may not use them anywhere; execution
// still re-checks the specific app through Deps.Authorize.
func Configurator(deps Deps) func(ctx context.Context, principal *services.DashboardPrincipal, server *mcpprot.Server) {
	return func(ctx context.Context, principal *services.DashboardPrincipal, server *mcpprot.Server) {
		for _, registration := range registrations {
			if registration.access != nil && !deps.CanUseSomewhere(ctx, principal, *registration.access) {
				continue
			}
			registration.register(server, deps)
		}
	}
}
