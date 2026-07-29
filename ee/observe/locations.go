// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Where the fleet is, read from the Postgres registry so the map and
// active-install count keep working on a deployment with no ClickHouse.
package observe

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"expo-open-ota/internal/database/postgres/pgdb"
)

// CheckInQuery narrows a check-in read; unlike ExplorerQuery it carries no telemetry dimensions.
type CheckInQuery struct {
	Since time.Time
	// From, To and Bucket are unused here: the cursor bounds the window instead.
	Dimensions ExplorerQuery
}

type ObserveLocation struct {
	CountryCode string  `json:"countryCode"`
	City        string  `json:"city"`
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
	DeviceCount uint64  `json:"deviceCount"`
}

// CheckInFeed is how many devices checked in per city over one poll window,
// aggregated like the static layer rather than one row per device.
type CheckInFeed struct {
	Cities []ObserveLocation `json:"cities"`
	// Cursor is what the client sends back as `since` on its next poll; server-owned to avoid clock skew.
	Cursor time.Time `json:"cursor"`
	// WindowSeconds is the span the counts cover, since the server clamps `since`.
	WindowSeconds float64 `json:"windowSeconds"`
	// Truncated says the window hit the city cap, so the quietest cities were dropped.
	Truncated bool `json:"truncated"`
}

// observeLocationLimit mirrors the LIMIT of ListObserveLocations.
const observeLocationLimit = 500

// locationParams builds the map query. Build dimensions (channel, EAS build,
// environment) are absent: the registry never learns them.
func (e *Explorer) locationParams(
	appID string,
	activeSince time.Time,
	query ExplorerQuery,
) (pgdb.ListObserveLocationsParams, error) {
	appUUID, err := toPGUUID(appID)
	if err != nil {
		return pgdb.ListObserveLocationsParams{}, err
	}
	clientIDs, err := toPGUUIDs(query.EASClientIDs)
	if err != nil {
		return pgdb.ListObserveLocationsParams{}, err
	}
	updateIDs, err := toPGUUIDs(query.UpdateIDs)
	if err != nil {
		return pgdb.ListObserveLocationsParams{}, err
	}
	publishGroups, err := toPGUUIDs(query.UpdateGroupIDs)
	if err != nil {
		return pgdb.ListObserveLocationsParams{}, err
	}
	return pgdb.ListObserveLocationsParams{
		AppID:           appUUID,
		ActiveSince:     pgtype.Timestamptz{Time: activeSince.UTC(), Valid: true},
		Filters:         query.MetadataFilter,
		EasClientID:     clientIDs,
		CurrentUpdateID: updateIDs,
		PublishGroup:    publishGroups,
		DeviceModel:     query.DeviceModels,
		OsName:          query.OSNames,
		OsVersion:       query.OSVersions,
		CountryCode:     query.CountryCodes,
		Branch:          query.Branches,
		RuntimeVersion:  query.RuntimeVersions,
		Platform:        query.Platform,
	}, nil
}

func (e *Explorer) locations(
	ctx context.Context,
	appID string,
	activeSince time.Time,
	query ExplorerQuery,
) ([]ObserveLocation, error) {
	params, err := e.locationParams(appID, activeSince, query)
	if err != nil {
		return nil, err
	}
	return e.runLocations(ctx, params)
}

// cachedLocations serves the map's static layer: every device seen since the
// start of the selected period, cached since the dashboard refetches it on a timer.
func (e *Explorer) cachedLocations(
	ctx context.Context,
	appID string,
	activeSince time.Time,
	query ExplorerQuery,
) ([]ObserveLocation, error) {
	params, err := e.locationParams(appID, activeSince, query)
	if err != nil {
		return nil, err
	}

	return cachedRead(
		ctx,
		readCacheKey("map", params),
		func(ctx context.Context) ([]ObserveLocation, error) { return e.runLocations(ctx, params) })
}

func (e *Explorer) runLocations(
	ctx context.Context,
	params pgdb.ListObserveLocationsParams,
) ([]ObserveLocation, error) {
	rows, err := e.postgres.ListObserveLocations(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("listing observe locations: %w", err)
	}
	locations := make([]ObserveLocation, 0, len(rows))
	for _, row := range rows {
		if row.Lat == nil || row.Lng == nil {
			continue
		}
		location := ObserveLocation{
			Lat:         *row.Lat,
			Lng:         *row.Lng,
			DeviceCount: uint64(max(row.DeviceCount, 0)),
		}
		if row.CountryCode != nil {
			location.CountryCode = *row.CountryCode
		}
		if row.City != nil {
			location.City = *row.City
		}
		locations = append(locations, location)
	}
	return locations, nil
}

// ReadCheckIns serves the live feed of the map: the same city aggregate as
// the static layer, over the window since the caller's cursor.
func (e *Explorer) ReadCheckIns(ctx context.Context, appID string, query CheckInQuery) (CheckInFeed, error) {
	// Stamped before the query so a concurrent check-in falls in the next window rather than vanishing.
	cursor := time.Now().UTC()
	cities, err := e.locations(ctx, appID, query.Since, query.Dimensions)
	if err != nil {
		return CheckInFeed{}, err
	}
	return CheckInFeed{
		Cities:        cities,
		Cursor:        cursor,
		WindowSeconds: cursor.Sub(query.Since).Seconds(),
		Truncated:     len(cities) >= observeLocationLimit,
	}, nil
}

func (e *Explorer) activeUsers(ctx context.Context, appID string, query ExplorerQuery) (uint64, error) {
	appUUID, err := toPGUUID(appID)
	if err != nil {
		return 0, err
	}
	clientIDs, err := toPGUUIDs(query.EASClientIDs)
	if err != nil {
		return 0, err
	}
	updateIDs, err := toPGUUIDs(query.UpdateIDs)
	if err != nil {
		return 0, err
	}
	publishGroups, err := toPGUUIDs(query.UpdateGroupIDs)
	if err != nil {
		return 0, err
	}
	count, err := e.postgres.CountObserveActiveDevices(ctx, pgdb.CountObserveActiveDevicesParams{
		AppID:           appUUID,
		ActiveSince:     pgtype.Timestamptz{Time: query.From.UTC(), Valid: true},
		Filters:         query.MetadataFilter,
		EasClientID:     clientIDs,
		CurrentUpdateID: updateIDs,
		PublishGroup:    publishGroups,
		DeviceModel:     query.DeviceModels,
		OsName:          query.OSNames,
		OsVersion:       query.OSVersions,
		CountryCode:     query.CountryCodes,
		Branch:          query.Branches,
		RuntimeVersion:  query.RuntimeVersions,
		Platform:        query.Platform,
	})
	if err != nil {
		return 0, fmt.Errorf("counting active observe devices: %w", err)
	}
	return uint64(max(count, 0)), nil
}
