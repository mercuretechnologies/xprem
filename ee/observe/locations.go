// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Where the fleet is, read from the Postgres registry. Apart from explorer.go
// because it is the one part of the explorer that never touches ClickHouse,
// which is exactly why it exists: the map and the active-install count keep
// working on a deployment that stores no telemetry at all.
package observe

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"expo-open-ota/internal/database/postgres/pgdb"
)

// CheckInQuery is deliberately not an ExplorerQuery: the registry only knows a
// device's identity metadata and where it last checked in from, so carrying
// the telemetry dimensions here would advertise filters this read cannot
// honor. Same reasoning as the locations aggregate it feeds.
type CheckInQuery struct {
	Since time.Time
	// The narrowing of the page the map sits on. From, To and Bucket are unused
	// here: the cursor is what bounds a check-in window.
	Dimensions ExplorerQuery
}

type ObserveLocation struct {
	CountryCode string  `json:"countryCode"`
	City        string  `json:"city"`
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
	DeviceCount uint64  `json:"deviceCount"`
}

// CheckInFeed is what the map animates: how many devices checked in per city
// over one poll window. Deliberately the same aggregate as the static layer
// rather than one row per device. A per-device feed would be unbounded (a
// fleet in the millions produces tens of thousands of check-ins in the few
// seconds between two polls) to describe an animation that can only ever
// render a handful of ripples per city.
type CheckInFeed struct {
	Cities []ObserveLocation `json:"cities"`
	// Cursor is what the client must send back as `since` on its next poll.
	// Server-owned on purpose: a browser clock running fast would skip a
	// window of check-ins, one running slow would replay it forever.
	Cursor time.Time `json:"cursor"`
	// WindowSeconds is the span the counts cover. The client needs it because
	// the server clamps `since`, so the window is not always the interval the
	// client thinks it polled at, and the ripples are spread over it.
	WindowSeconds float64 `json:"windowSeconds"`
	// Truncated says the window hit the city cap, so the quietest cities were
	// dropped. The map says so rather than showing a world that went silent.
	Truncated bool `json:"truncated"`
}

// observeLocationLimit mirrors the LIMIT of ListObserveLocations.
const observeLocationLimit = 500

// The map answers to the same dimensions as the inventory it draws. Build
// dimensions (channel, app version, EAS build, environment) are deliberately
// absent: the registry never learns them, they exist only on telemetry rows.
func (e *Explorer) locations(
	ctx context.Context,
	appID string,
	activeSince time.Time,
	query ExplorerQuery,
) ([]ObserveLocation, error) {
	appUUID, err := toPGUUID(appID)
	if err != nil {
		return nil, err
	}
	clientIDs, err := toPGUUIDs(query.EASClientIDs)
	if err != nil {
		return nil, err
	}
	updateIDs, err := toPGUUIDs(query.UpdateIDs)
	if err != nil {
		return nil, err
	}
	publishGroups, err := toPGUUIDs(query.UpdateGroupIDs)
	if err != nil {
		return nil, err
	}
	rows, err := e.postgres.ListObserveLocations(ctx, pgdb.ListObserveLocationsParams{
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
	})
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

// ReadCheckIns serves the live feed of the map: the same city aggregate as the
// static layer, over the few seconds since the caller's cursor instead of over
// the period. It reads the registry only, so the map keeps beating on a
// deployment that runs expo-open-ota without telemetry at all.
func (e *Explorer) ReadCheckIns(ctx context.Context, appID string, query CheckInQuery) (CheckInFeed, error) {
	// Stamped before the query so a check-in written while it runs falls in
	// the next window instead of vanishing between the two.
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
