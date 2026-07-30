// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"xprem/internal/cache"

	"golang.org/x/sync/singleflight"
)

// readComputeTimeout bounds how long a collapsed computation may run once it
// no longer belongs to any one request.
const readComputeTimeout = 30 * time.Second

// readCacheTTLSeconds is how long one answer is reused, matching the
// dashboard's tightest poll cadence.
const readCacheTTLSeconds = 5

// cachedRead serves an expensive aggregate from the shared cache, computing it
// only when no recent identical answer exists. Not used for the log tail or
// check-in feed, since both are cursor-paginated and never share a key.
func cachedRead[T any](ctx context.Context, key string, compute func(context.Context) (T, error)) (T, error) {
	store := cache.GetCache()
	if payload := store.Get(key); payload != "" {
		var cached T
		if json.Unmarshal([]byte(payload), &cached) == nil {
			return cached, nil
		}
	}

	// The computed context is detached from ctx: one caller's cancellation must
	// not fail every other request collapsed onto this computation.
	fresh, err, _ := readFlight.Do(key, func() (any, error) {
		computeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readComputeTimeout)
		defer cancel()
		if payload := store.Get(key); payload != "" {
			var cached T
			if json.Unmarshal([]byte(payload), &cached) == nil {
				return cached, nil
			}
		}
		computed, err := compute(computeCtx)
		if err != nil {
			return computed, err
		}
		if payload, err := json.Marshal(computed); err == nil {
			ttl := readCacheTTLSeconds
			_ = store.Set(key, string(payload), &ttl)
		}
		return computed, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	typed, ok := fresh.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("cached read %q returned %T", key, fresh)
	}
	return typed, nil
}

// readFlight collapses concurrent misses of the same key.
var readFlight singleflight.Group

// readCacheKey fingerprints whatever identifies one read; every value the
// query depends on must be included.
func readCacheKey(name string, parts ...any) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%v", parts)))
	return "observe:read:" + name + ":" + hex.EncodeToString(sum[:])
}
