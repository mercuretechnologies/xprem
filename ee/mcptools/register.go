// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	mittools "expo-open-ota/internal/mcptools"
	"expo-open-ota/internal/services"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Deps carries what the enterprise tools need from the composition root;
// it grows with the tool set. Like its MIT twin, every field is a plain
// method value assigned in wire.
type Deps struct {
	// CanUseSomewhere gates enterprise tool visibility, shared with the MIT
	// table's vocabulary.
	CanUseSomewhere func(ctx context.Context, principal *services.DashboardPrincipal, access mittools.Access) bool
}

// registrations is the enterprise tool table, the ee twin of the MIT one.
// The license gate belongs in each tool's execution, not here: a license can
// be activated or expire while sessions live.
var registrations = []struct {
	register func(*mcpprot.Server, Deps)
	access   *mittools.Access
}{}

// Configurator populates one session's server with the enterprise tools its
// principal may use.
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
