// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"

	"xprem/internal/database/postgres/pgdb"
)

// fleetDimensions are the facets of a fleet read, in the order they are returned.
var fleetDimensions = []string{
	"channel", "runtimeVersion", "update", "platform", "appVersion", "deviceModel", "osVersion", "country",
}

// fleetFacetSize is how many values a facet lists before folding the rest into Others.
const fleetFacetSize = 50

// FleetValue is one value of a facet. An empty Value means the registry has not recorded it.
type FleetValue struct {
	Value string `json:"value"`
	// Context qualifies Value: the OS name of an OS version, or "group" / "update" for an update.
	Context string `json:"context,omitempty"`
	Devices uint64 `json:"devices"`
}

// FleetFacet is how the active fleet splits along one dimension.
type FleetFacet struct {
	Dimension string       `json:"dimension"`
	Values    []FleetValue `json:"values"`
	// Others is the devices behind the OtherValues values that did not make the list.
	Others      uint64 `json:"others"`
	OtherValues int    `json:"otherValues"`
}

// Fleet is the active device registry of an app, split along every fleet dimension.
type Fleet struct {
	Available bool         `json:"available"`
	Devices   uint64       `json:"devices"`
	Facets    []FleetFacet `json:"facets"`
}

// ChannelAdoption is how many active devices of a channel already run what it serves them.
type ChannelAdoption struct {
	Channel         string `json:"channel"`
	ActiveDevices   uint64 `json:"activeDevices"`
	EmbeddedDevices uint64 `json:"embeddedDevices"`
	UpToDateDevices uint64 `json:"upToDateDevices"`
}

type Releases struct {
	Available bool              `json:"available"`
	Channels  []ChannelAdoption `json:"channels"`
}

func (e *Explorer) ReadFleet(ctx context.Context, appID string, query ExplorerQuery) (Fleet, error) {
	return cachedRead(ctx, readCacheKey("fleet", appID, query), func(ctx context.Context) (Fleet, error) {
		return e.readFleet(ctx, appID, query)
	})
}

func (e *Explorer) readFleet(ctx context.Context, appID string, query ExplorerQuery) (Fleet, error) {
	location, err := e.locationParams(appID, query.From, query)
	if err != nil {
		return Fleet{}, err
	}
	rows, err := e.postgres.ListObserveFleetFacets(ctx, pgdb.ListObserveFleetFacetsParams(registryFilters(location, query)))
	if err != nil {
		return Fleet{}, fmt.Errorf("listing observe fleet: %w", err)
	}
	return buildFleet(rows), nil
}

// registryFilterParams is the filter set both fleet queries take, field for field, so it
// converts to either generated params struct.
type registryFilterParams struct {
	AppID           pgtype.UUID
	ActiveSince     pgtype.Timestamptz
	Filters         [][]byte
	EasClientID     []pgtype.UUID
	CurrentUpdateID []pgtype.UUID
	PublishGroup    []pgtype.UUID
	DeviceModel     []string
	OsName          []string
	OsVersion       []string
	CountryCode     []string
	Branch          []string
	RuntimeVersion  []string
	Platform        []string
	Channel         []string
	AppVersion      []string
}

func registryFilters(location pgdb.ListObserveLocationsParams, query ExplorerQuery) registryFilterParams {
	return registryFilterParams{
		AppID:           location.AppID,
		ActiveSince:     location.ActiveSince,
		Filters:         location.Filters,
		EasClientID:     location.EasClientID,
		CurrentUpdateID: location.CurrentUpdateID,
		PublishGroup:    location.PublishGroup,
		DeviceModel:     location.DeviceModel,
		OsName:          location.OsName,
		OsVersion:       location.OsVersion,
		CountryCode:     location.CountryCode,
		Branch:          location.Branch,
		RuntimeVersion:  location.RuntimeVersion,
		Platform:        location.Platform,
		Channel:         query.Channels,
		AppVersion:      query.AppVersions,
	}
}

func buildFleet(rows []pgdb.ListObserveFleetFacetsRow) Fleet {
	values := make(map[string][]FleetValue, len(fleetDimensions))
	for _, row := range rows {
		values[row.Dimension] = append(values[row.Dimension], FleetValue{
			Value:   row.Value,
			Context: row.Context,
			Devices: uint64(max(row.Devices, 0)),
		})
	}
	fleet := Fleet{Available: true, Facets: make([]FleetFacet, 0, len(fleetDimensions))}
	// Every device counts exactly once in each facet, so any one of them sums to the fleet.
	for _, value := range values["platform"] {
		fleet.Devices += value.Devices
	}
	for _, dimension := range fleetDimensions {
		ranked := values[dimension]
		sort.Slice(ranked, func(i, j int) bool {
			if ranked[i].Devices != ranked[j].Devices {
				return ranked[i].Devices > ranked[j].Devices
			}
			return ranked[i].Value < ranked[j].Value
		})
		facet := FleetFacet{Dimension: dimension, Values: ranked}
		if len(ranked) > fleetFacetSize {
			facet.Values = ranked[:fleetFacetSize]
			for _, folded := range ranked[fleetFacetSize:] {
				facet.Others += folded.Devices
			}
			facet.OtherValues = len(ranked) - fleetFacetSize
		}
		if facet.Values == nil {
			facet.Values = []FleetValue{}
		}
		fleet.Facets = append(fleet.Facets, facet)
	}
	return fleet
}

func (e *Explorer) ReadReleases(ctx context.Context, appID string, query ExplorerQuery) (Releases, error) {
	return cachedRead(ctx, readCacheKey("releases", appID, query), func(ctx context.Context) (Releases, error) {
		return e.readReleases(ctx, appID, query)
	})
}

func (e *Explorer) readReleases(ctx context.Context, appID string, query ExplorerQuery) (Releases, error) {
	location, err := e.locationParams(appID, query.From, query)
	if err != nil {
		return Releases{}, err
	}
	rows, err := e.postgres.ListObserveChannelAdoption(ctx, pgdb.ListObserveChannelAdoptionParams(registryFilters(location, query)))
	if err != nil {
		return Releases{}, fmt.Errorf("listing observe channel adoption: %w", err)
	}
	releases := Releases{Available: true, Channels: make([]ChannelAdoption, 0, len(rows))}
	for _, row := range rows {
		releases.Channels = append(releases.Channels, ChannelAdoption{
			Channel:         row.ChannelName,
			ActiveDevices:   uint64(max(row.ActiveDevices, 0)),
			EmbeddedDevices: uint64(max(row.EmbeddedDevices, 0)),
			UpToDateDevices: uint64(max(row.UpToDateDevices, 0)),
		})
	}
	return releases, nil
}
