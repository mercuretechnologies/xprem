// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	"xprem/ee/audit"
	"xprem/ee/identity"
	"xprem/ee/observe"
	mittools "xprem/internal/mcptools"
	"xprem/internal/services"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// identityUpdateHealth is what the device registry answers per update; aliased
// so the health tool does not repeat the store's package path.
type identityUpdateHealth = identity.UpdateHealth

// Deps carries what the enterprise tools need from the composition root;
// it grows with the tool set. Like its MIT twin, every field is a plain
// method value or service assigned in wire.
type Deps struct {
	// CanUseSomewhere gates enterprise tool visibility, shared with the MIT
	// table's vocabulary.
	CanUseSomewhere func(ctx context.Context, principal *services.DashboardPrincipal, access mittools.Access) bool
	// Authorize gates one tool execution on one app. CanUseSomewhere above
	// only decides what a session sees: holding a permission on some app must
	// never unlock another one.
	Authorize func(ctx context.Context, principal *services.DashboardPrincipal, appID string, access mittools.Access) error
	Audit     *audit.AuditService
	// Apps and VisibleApps gate the app-scoped enterprise tools through the
	// shared MIT decision.
	Apps        mittools.AppLister
	VisibleApps mittools.AppVisibilityFunc
	// Identity reads the instant-T device registry; nil in stateless mode and
	// under DISABLE_DEVICE_TELEMETRY.
	Identity *identity.Service
	// HealthHistory serves the snapshot history; also nil without ClickHouse,
	// in which case StateHistory serves the degraded arrival history.
	HealthHistory *observe.HealthHistory
	StateHistory  *observe.StateHistory
	// UpdateFeed resolves a publish group into its updates.
	UpdateFeed mittools.UpdateFeedReader
	// Explorer reads the telemetry: logs, events and timings. Nil in
	// stateless mode and under DISABLE_DEVICE_TELEMETRY; present but serving
	// degraded answers without ClickHouse.
	Explorer *observe.Explorer
}

// registrations is the enterprise tool table, the ee twin of the MIT one.
// The license gate belongs in each tool's execution, not here: a license can
// be activated or expire while sessions live.
var registrations = []struct {
	register func(*mcpprot.Server, Deps)
	access   *mittools.Access
}{
	{register: registerQueryAuditLogs, access: &mittools.Access{Fallback: mittools.FallbackAdminOnly}},
	// Its dashboard twin is AnyViewer: seeing the app is enough.
	{register: registerGetUpdateHealth},
	{register: registerSearchDevices, access: &identityAccess},
	{register: registerGetDevice, access: &identityAccess},
	{register: registerCountOnlineDevices, access: &identityAccess},
	{register: registerGetDeviceAttributes, access: &identityAccess},
	{register: registerQueryLogs, access: &observeAccess},
	{register: registerGetObserveOverview, access: &observeAccess},
	{register: registerGetMetricBreakdown, access: &observeAccess},
	{register: registerGetObserveEvents, access: &observeAccess},
}

// DeclaredPermissions lists the permission strings this table gates on, for
// the boot-time catalog check (see rbac.MustValidateMCPTools).
func DeclaredPermissions() []string {
	perms := make([]string, 0, len(registrations))
	for _, registration := range registrations {
		if registration.access != nil && registration.access.Perm != "" {
			perms = append(perms, registration.access.Perm)
		}
	}
	return perms
}

// Configurator populates one session's server with the enterprise tools its
// principal may use.
func Configurator(deps Deps) func(ctx context.Context, principal *services.DashboardPrincipal, server *mcpprot.Server) {
	return func(ctx context.Context, principal *services.DashboardPrincipal, server *mcpprot.Server) {
		canUse := mittools.MemoizeVisibility(deps.CanUseSomewhere)
		for _, registration := range registrations {
			if registration.access != nil && !canUse(ctx, principal, *registration.access) {
				continue
			}
			registration.register(server, deps)
		}
	}
}
