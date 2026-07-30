// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	"errors"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"xprem/ee/identity"
	"xprem/ee/observe"
	"xprem/ee/rbac"
	mittools "xprem/internal/mcptools"

	"github.com/google/uuid"
	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// boundedRead gives a tool read the same ceiling the HTTP handlers give theirs.
func boundedRead(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, telemetryReadTimeout)
}

// observeAccess is the explorer routes' own permission, fallback included.
var observeAccess = rbac.MCPAccess(rbac.PermObserveRead)

const (
	// The route twins cap every filter list and every value; a tool that did
	// not would let one call fan out into thousands of reads (one Postgres
	// round trip per publish group, one IN argument per value).
	maxFilterValues      = 25
	maxFilterValueLength = 256
	// The device routes are looser than the explorer's.
	maxDeviceFilterValues = 100
	// telemetryReadTimeout bounds a tool read the way boundedRead bounds the
	// route: an agent that walks away must not leave a ClickHouse scan running.
	telemetryReadTimeout = 30 * time.Second

	defaultObserveWindow = 24 * time.Hour
	maxObserveWindow     = 90 * 24 * time.Hour
	// Logs are kept for a shorter window than the aggregates.
	maxLogsWindow = 31 * 24 * time.Hour
)

// ObserveFilters narrows every telemetry read. Each list is a disjunction and
// the lists combine with AND, exactly like the dashboard's filter bar.
type ObserveFilters struct {
	Platforms       []string `json:"platforms,omitempty" jsonschema:"ios or android"`
	UpdateIds       []string `json:"updateIds,omitempty" jsonschema:"update uuids, as returned by get_updates"`
	UpdateGroupIds  []string `json:"updateGroupIds,omitempty" jsonschema:"publish group uuids, as returned by get_updates"`
	Branches        []string `json:"branches,omitempty" jsonschema:"branch names, as returned by get_branches"`
	RuntimeVersions []string `json:"runtimeVersions,omitempty"`
	Channels        []string `json:"channels,omitempty" jsonschema:"release channel names, as returned by get_channels"`
	EasClientIds    []string `json:"easClientIds,omitempty" jsonschema:"install uuids, as returned by search_devices"`
	AppVersions     []string `json:"appVersions,omitempty" jsonschema:"the app version of the binary, not the update"`
	AppBuildNumbers []string `json:"appBuildNumbers,omitempty"`
	EasBuildIds     []string `json:"easBuildIds,omitempty"`
	Environments    []string `json:"environments,omitempty"`
	OsNames         []string `json:"osNames,omitempty"`
	OsVersions      []string `json:"osVersions,omitempty"`
	DeviceModels    []string `json:"deviceModels,omitempty"`
	CountryCodes    []string `json:"countryCodes,omitempty" jsonschema:"the country recorded when the measurement was taken, not where the device is now"`
	Attributes      []string `json:"attributes,omitempty" jsonschema:"operator-defined attributes as key:value pairs; list them with get_device_attributes"`
	// The five conditions below narrow to the state the device was in when it
	// took the measurement. Only get_observe_overview and
	// get_metric_breakdown honor them; the other tools refuse them rather
	// than ignore them.
	ThermalState []string `json:"thermalState,omitempty" jsonschema:"nominal, fair, serious or critical"`
	LowPowerMode []string `json:"lowPowerMode,omitempty" jsonschema:"Normal power or Low power mode"`
	NetworkType  []string `json:"networkType,omitempty" jsonschema:"wifi, cellular or none"`
	FrozenFrames []string `json:"frozenFrames,omitempty" jsonschema:"None, 1 to 2, 3 to 8, 9 to 20 or Over 20"`
	NetworkBytes []string `json:"networkBytes,omitempty" jsonschema:"Under 100 kB, 100 to 500 kB, 500 kB to 2 MB or Over 2 MB"`
}

// conditions returns the device-state filters that were asked for.
func (filters ObserveFilters) conditions() map[string][]string {
	asked := map[string][]string{}
	for name, values := range map[string][]string{
		"thermalState": filters.ThermalState,
		"lowPowerMode": filters.LowPowerMode,
		"networkType":  filters.NetworkType,
		"frozenFrames": filters.FrozenFrames,
		"networkBytes": filters.NetworkBytes,
	} {
		if len(values) > 0 {
			asked[name] = values
		}
	}
	return asked
}

// rejectConditions is called by the reads that cannot honor them: silently
// dropping a filter would answer a different question than the one asked.
func (filters ObserveFilters) rejectConditions() error {
	asked := filters.conditions()
	if len(asked) == 0 {
		return nil
	}
	names := make([]string, 0, len(asked))
	for name := range asked {
		names = append(names, name)
	}
	sort.Strings(names)
	return errors.New(strings.Join(names, ", ") + " only narrow the timing reads (get_observe_overview, get_metric_breakdown); this tool cannot honor them")
}

// validateFilterList applies the caps the route twin applies: a bounded number
// of bounded values, and real uuids where uuids are expected.
func validateFilterList(name string, values []string, maximum int, asUUID bool) error {
	if len(values) > maximum {
		return errors.New(name + " carries too many values: " + strconv.Itoa(maximum) + " at most")
	}
	for _, value := range values {
		if len(value) > maxFilterValueLength {
			return errors.New(name + " has a value longer than " + strconv.Itoa(maxFilterValueLength) + " characters")
		}
		if asUUID {
			if _, err := uuid.Parse(value); err != nil {
				return errors.New(name + " must contain uuids")
			}
		}
	}
	return nil
}

// validate bounds every list before it reaches a query builder.
func (filters ObserveFilters) validate() error {
	for _, list := range []struct {
		name   string
		values []string
		uuid   bool
	}{
		{"updateIds", filters.UpdateIds, true},
		{"updateGroupIds", filters.UpdateGroupIds, true},
		{"easClientIds", filters.EasClientIds, true},
		{"easBuildIds", filters.EasBuildIds, true},
		{"platforms", filters.Platforms, false},
		{"branches", filters.Branches, false},
		{"runtimeVersions", filters.RuntimeVersions, false},
		{"channels", filters.Channels, false},
		{"appVersions", filters.AppVersions, false},
		{"appBuildNumbers", filters.AppBuildNumbers, false},
		{"environments", filters.Environments, false},
		{"osNames", filters.OsNames, false},
		{"osVersions", filters.OsVersions, false},
		{"deviceModels", filters.DeviceModels, false},
		{"countryCodes", filters.CountryCodes, false},
		{"attributes", filters.Attributes, false},
		{"thermalState", filters.ThermalState, false},
		{"lowPowerMode", filters.LowPowerMode, false},
		{"networkType", filters.NetworkType, false},
		{"frozenFrames", filters.FrozenFrames, false},
		{"networkBytes", filters.NetworkBytes, false},
	} {
		if err := validateFilterList(list.name, list.values, maxFilterValues, list.uuid); err != nil {
			return err
		}
	}
	for _, platform := range filters.Platforms {
		if platform != "ios" && platform != "android" {
			return errors.New("platforms must contain ios or android")
		}
	}
	return nil
}

// explorerQuery builds the service query: the window, the dimensions, and the
// attribute cohort, which is resolved against the app's declared schema so an
// undeclared key names itself instead of matching nothing.
func (deps Deps) explorerQuery(ctx context.Context, appID string, filters ObserveFilters, rawFrom string, rawTo string, maxWindow time.Duration) (observe.ExplorerQuery, error) {
	if err := filters.validate(); err != nil {
		return observe.ExplorerQuery{}, err
	}
	from, to, err := observeWindow(rawFrom, rawTo, maxWindow)
	if err != nil {
		return observe.ExplorerQuery{}, err
	}
	query := observe.ExplorerQuery{
		From:            from,
		To:              to,
		Platform:        filters.Platforms,
		UpdateIDs:       filters.UpdateIds,
		UpdateGroupIDs:  filters.UpdateGroupIds,
		Branches:        filters.Branches,
		RuntimeVersions: filters.RuntimeVersions,
		Channels:        filters.Channels,
		EASClientIDs:    filters.EasClientIds,
		AppVersions:     filters.AppVersions,
		AppBuildNumbers: filters.AppBuildNumbers,
		EASBuildIDs:     filters.EasBuildIds,
		Environments:    filters.Environments,
		OSNames:         filters.OsNames,
		OSVersions:      filters.OsVersions,
		DeviceModels:    filters.DeviceModels,
		CountryCodes:    filters.CountryCodes,
		Bucket:          observe.Bucket(to.Sub(from)),
	}
	if conditions := filters.conditions(); len(conditions) > 0 {
		query.Conditions = conditions
	}
	if len(filters.Attributes) == 0 {
		return query, nil
	}

	if deps.Identity == nil {
		return observe.ExplorerQuery{}, errors.New("this deployment records no device attributes, so they cannot be filtered on")
	}
	schema, err := deps.Identity.GetSchema(ctx, appID)
	if err != nil {
		log.Printf("mcp observe tools could not read the attribute schema of app %s: %v", appID, err)
		return observe.ExplorerQuery{}, errors.New("could not read the attribute schema, try again later")
	}
	metadata, err := identity.ParseFilterPairs(schema, filters.Attributes)
	if err != nil {
		return observe.ExplorerQuery{}, errors.New("attributes must be key:value pairs of declared attributes; list them with get_device_attributes")
	}
	docs, err := metadata.ContainmentDocs()
	if err != nil {
		return observe.ExplorerQuery{}, errors.New("too many attribute combinations; narrow the attributes filter")
	}
	query.MetadataFilter = docs
	return query, nil
}

func observeWindow(rawFrom string, rawTo string, maxWindow time.Duration) (time.Time, time.Time, error) {
	to := time.Now().UTC()
	if rawTo != "" {
		parsed, err := time.Parse(time.RFC3339, rawTo)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("to must be an RFC3339 timestamp")
		}
		to = parsed.UTC()
	}
	from := to.Add(-defaultObserveWindow)
	if rawFrom != "" {
		parsed, err := time.Parse(time.RFC3339, rawFrom)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("from must be an RFC3339 timestamp")
		}
		from = parsed.UTC()
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, errors.New("from must be earlier than to")
	}
	if to.Sub(from) > maxWindow {
		return time.Time{}, time.Time{}, errors.New("the window is too wide for this read: " + maxWindow.String() + " at most")
	}
	return from, to, nil
}

// requireTelemetry is the shared gate of the explorer tools: a caller holding
// observe:read on this app, on a deployment that collects telemetry.
func (deps Deps) requireTelemetry(ctx context.Context, req *mcpprot.CallToolRequest, appID string) error {
	if err := deps.requireAppPermission(ctx, req, appID, observeAccess); err != nil {
		return err
	}
	if deps.Explorer == nil {
		return errors.New("this deployment collects no telemetry (stateless mode, or device telemetry disabled)")
	}
	return nil
}

// requireAppPermission resolves the caller, checks the app is visible, and
// authorizes the action ON THAT APP. The access a tool declares in the table
// only decides whether the session sees it; a member holding a permission on
// one app must not reach another through it.
func (deps Deps) requireAppPermission(ctx context.Context, req *mcpprot.CallToolRequest, appID string, access mittools.Access) error {
	principal := mittools.PrincipalFromRequest(req)
	if principal == nil {
		return errors.New("no authenticated account on this session")
	}
	if err := mittools.RequireAppAccess(ctx, principal, appID, deps.Apps, deps.VisibleApps); err != nil {
		return err
	}
	if deps.Authorize == nil {
		return errors.New("authorization is unavailable on this deployment")
	}
	return deps.Authorize(ctx, principal, appID, access)
}
