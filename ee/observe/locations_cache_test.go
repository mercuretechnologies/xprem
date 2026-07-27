// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"testing"
	"time"

	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres"
	"expo-open-ota/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func mapParams(t *testing.T, appID string, activeSince time.Time, query ExplorerQuery) pgdb.ListObserveLocationsParams {
	t.Helper()
	explorer := &Explorer{}
	params, err := explorer.locationParams(appID, activeSince, query)
	require.NoError(t, err)
	return params
}

// The whole point of caching this one: two viewers looking at the same app,
// the same period and the same filters must land on the same entry. They do
// without any effort on our part, because the dashboard snaps the window start
// to a grid and sends no `to`, so their queries are byte for byte identical.
func TestMapCacheKeyIsSharedByIdenticalQueries(t *testing.T) {
	appID := uuid.NewString()
	from := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	query := ExplorerQuery{Platform: []string{"ios"}, Branches: []string{"production"}}

	first := readCacheKey("map", mapParams(t, appID, from, query))
	second := readCacheKey("map", mapParams(t, appID, from, query))
	require.Equal(t, first, second)
}

// And the failure that would matter: serving one filter set the answer that
// belongs to another. Every dimension the query carries has to move the key,
// so this walks them one at a time rather than trusting that it does.
func TestMapCacheKeySeparatesEveryDimension(t *testing.T) {
	appID := uuid.NewString()
	from := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	base := ExplorerQuery{}
	baseKey := readCacheKey("map", mapParams(t, appID, from, base))

	for name, altered := range map[string]ExplorerQuery{
		"platform":        {Platform: []string{"ios"}},
		"branch":          {Branches: []string{"production"}},
		"runtime version": {RuntimeVersions: []string{"1.0.0"}},
		"device model":    {DeviceModels: []string{"iPhone18,2"}},
		"os name":         {OSNames: []string{"iOS"}},
		"os version":      {OSVersions: []string{"26.1"}},
		"country":         {CountryCodes: []string{"FR"}},
		"update id":       {UpdateIDs: []string{uuid.NewString()}},
		"update group":    {UpdateGroupIDs: []string{uuid.NewString()}},
		"client id":       {EASClientIDs: []string{uuid.NewString()}},
		"metadata":        {MetadataFilter: [][]byte{[]byte(`{"plan":"pro"}`)}},
	} {
		t.Run(name, func(t *testing.T) {
			require.NotEqual(t, baseKey, readCacheKey("map", mapParams(t, appID, from, altered)),
				"filtering on %s must not be served the unfiltered answer", name)
		})
	}
}

// Two apps are two answers, and the window is part of the question: the
// dashboard advances `from` once per grid step, and that step has to be a
// cache miss or the map would freeze a period behind.
func TestMapCacheKeySeparatesAppsAndWindows(t *testing.T) {
	from := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	query := ExplorerQuery{}
	appA, appB := uuid.NewString(), uuid.NewString()

	require.NotEqual(t,
		readCacheKey("map", mapParams(t, appA, from, query)),
		readCacheKey("map", mapParams(t, appB, from, query)),
		"one app must never be served another app's fleet")

	require.NotEqual(t,
		readCacheKey("map", mapParams(t, appA, from, query)),
		readCacheKey("map", mapParams(t, appA, from.Add(time.Minute), query)),
		"the next grid step must recompute")
}

// The behaviour, not just the key: a second read inside the window must be
// served from the cache. Proved by changing the underlying data between the
// two calls, which is the only way to tell "cached" apart from "queried again
// and got the same answer".
func TestCachedLocationsServesTheWindowFromCache(t *testing.T) {
	_, pgURL := requireLiveStores(t)
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(pgURL)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err)
	defer pool.Close()

	appID := uuid.NewString()
	_, err = pool.Exec(ctx, "INSERT INTO apps (id, name) VALUES ($1, $2)", appID, "map-"+appID[:8])
	require.NoError(t, err)
	defer func() { _, _ = pool.Exec(context.Background(), "DELETE FROM apps WHERE id = $1", appID) }()

	addDevice := func(city string) {
		_, err := pool.Exec(ctx, `
			INSERT INTO device_identity (app_id, eas_client_id, last_seen_at, city, country_code, lat, lng)
			VALUES ($1, $2, now(), $3, 'FR', 48.85, 2.35)`,
			appID, uuid.NewString(), city)
		require.NoError(t, err)
	}
	addDevice("Paris")

	explorer := NewExplorer(&database.Engine{Queries: pgdb.New(pool), DB: pool}, nil)
	from := time.Now().UTC().Add(-time.Hour)
	query := ExplorerQuery{}

	first, err := explorer.cachedLocations(ctx, appID, from, query)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Equal(t, uint64(1), first[0].DeviceCount)

	// A device arrives. Inside the window the map keeps its shape: the new
	// arrival reaches the viewer through ReadCheckIns, which is uncached, and
	// joins this aggregate at the next refresh.
	addDevice("Paris")
	second, err := explorer.cachedLocations(ctx, appID, from, query)
	require.NoError(t, err)
	require.Equal(t, uint64(1), second[0].DeviceCount,
		"a second read inside the window must come from the cache")

	// The uncached path sees the truth immediately, which is what keeps the
	// live feed honest while the aggregate lags.
	live, err := explorer.locations(ctx, appID, from, query)
	require.NoError(t, err)
	require.Equal(t, uint64(2), live[0].DeviceCount,
		"the check-in path is deliberately not cached")

	// A different filter set is a different question and must not be served
	// the entry above.
	filtered, err := explorer.cachedLocations(ctx, appID, from, ExplorerQuery{Platform: []string{"ios"}})
	require.NoError(t, err)
	require.Empty(t, filtered, "no device carries a platform, so the filtered answer is empty")
}
