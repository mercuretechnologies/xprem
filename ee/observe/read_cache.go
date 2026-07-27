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

	fresh, err := compute()
	if err != nil {
		return fresh, err
	}
	if payload, err := json.Marshal(fresh); err == nil {
		ttl := readCacheTTLSeconds
		// Best effort. A cache that refuses the write costs the next caller a
		// recompute, which is exactly where we were before.
		_ = store.Set(key, string(payload), &ttl)
	}
	return fresh, nil
}

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
