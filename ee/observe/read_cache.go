// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"expo-open-ota/internal/cache"

	"golang.org/x/sync/singleflight"
)

// readCacheTTLSeconds is how long one answer is reused. Five seconds is the
// poll cadence of the tightest period the dashboard offers ("last hour"), so a
// lone viewer never waits on a stale answer twice in a row while every extra
// viewer inside the window is served for free.
//
// It is a FLOOR on how often the expensive work runs, not a promise about
// freshness. The reads it covers aggregate the fleet: the segmented health
// grid, the breakdowns, the overview. Each is measured in hundreds of
// milliseconds to seconds on a large fleet, and the dashboard refetches them
// on a timer, per open tab. Without this, ten tabs are ten identical
// aggregations of the same rows every five seconds.
const readCacheTTLSeconds = 5

// cachedRead serves an expensive aggregate from the shared cache, computing it
// only when no recent identical answer exists.
//
// Sharing works without any effort on our part because the dashboard snaps its
// window start to a grid and leaves the end open: two people looking at the
// same app, period and filters send byte-identical queries. In production the
// cache is Redis, so the collapse is cluster-wide and ten tabs across five
// replicas still cost one aggregation.
//
// Deliberately NOT applied to the log tail or the check-in feed: both are
// cursor-paginated, so two viewers never share a key, and a tail that lags is
// a tail that reads as broken.
func cachedRead[T any](key string, compute func() (T, error)) (T, error) {
	store := cache.GetCache()
	if payload := store.Get(key); payload != "" {
		var cached T
		if json.Unmarshal([]byte(payload), &cached) == nil {
			return cached, nil
		}
		// Unreadable is treated as absent: a payload written by an older shape
		// must cost a recompute, never an error.
	}

	// One computation per key at a time. The entry expiring is not a quiet
	// moment: the dashboard refetches on a timer, so every open tab misses the
	// same key within milliseconds of each other and, without this, each one
	// started its own aggregation of the same rows. The cache collapses
	// SEQUENTIAL readers, this collapses SIMULTANEOUS ones, and the second is
	// the case that actually arises.
	//
	// Per process, not per cluster: replicas still compute once each. That is
	// the shape of the problem worth solving here, since the tabs that
	// synchronise are the ones behind a single connection.
	fresh, err, _ := readFlight.Do(key, func() (any, error) {
		// Re-read first: a caller that queued behind the leader wants the
		// answer it just wrote, not another aggregation of the same rows.
		if payload := store.Get(key); payload != "" {
			var cached T
			if json.Unmarshal([]byte(payload), &cached) == nil {
				return cached, nil
			}
		}
		computed, err := compute()
		if err != nil {
			return computed, err
		}
		if payload, err := json.Marshal(computed); err == nil {
			ttl := readCacheTTLSeconds
			// Best effort. A cache that refuses the write costs the next
			// caller a recompute, which is exactly where we were before.
			_ = store.Set(key, string(payload), &ttl)
		}
		return computed, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	// Do returns what the callback returned, so this is the same concrete type
	// on every path above.
	typed, ok := fresh.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("cached read %q returned %T", key, fresh)
	}
	return typed, nil
}

// readFlight collapses concurrent misses of the same key. Keyed by the same
// fingerprint as the cache entry, so two callers share a computation exactly
// when they would have shared its answer.
var readFlight singleflight.Group

// readCacheKey fingerprints whatever identifies one read. Everything the query
// depends on has to go in: a key that forgets a filter serves one question's
// answer to another, which is worse than the cost it saves.
//
// SHA-256 rather than something cheaper because that failure is silent and the
// hash is free next to the aggregation it skips.
func readCacheKey(name string, parts ...any) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%v", parts)))
	return "observe:read:" + name + ":" + hex.EncodeToString(sum[:])
}
