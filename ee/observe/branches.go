// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"strings"

	"xprem/internal/cache"
)

// BranchResolver names the branch an update belongs to and the publish it came
// from, "" for either when unknown.
type BranchResolver interface {
	UpdateOrigin(ctx context.Context, appID string, updateID string) (branch string, updateGroupID string)
}

// BranchLookup is the data access this package deliberately does not own:
// updates and branches are community-core tables, their SQL lives in
// internal/store. wire hands the Postgres update store's method value in
// (store.PostgresUpdateStore.GetUpdateOriginByUUID). ("", "", nil) means "no
// such update", permanent and cacheable; an error is transient trouble.
type BranchLookup func(ctx context.Context, appID string, updateUUID string) (branch string, updateGroupID string, err error)

// branchCacheTTLSeconds is not about freshness: the update->branch mapping is
// permanent. It bounds growth (a device spamming forged update ids must not
// accumulate keys forever, in Redis least of all) and lets deleted updates'
// negatives fall out; the cost is one indexed query per live update per hour.
const branchCacheTTLSeconds = 3600

// Cached values need a marker: cache.Get answers "" for a miss, so a negative
// ("no such update", cacheable and permanent) must be distinguishable from
// "not cached yet". Positives are prefixed, the negative is a constant, same
// pattern as the app-existence middleware above.
const (
	branchKnownValuePrefix  = "b"
	branchUnknownCacheValue = "0"
)

// CachingBranchResolver memoizes lookups in the process cache (shared across
// replicas when CACHE_MODE is redis). Negative results are cached too: an
// update the server does not know now (deleted, or forged) will not exist
// later either, update ids are never recycled.
type CachingBranchResolver struct {
	cache  cache.Cache
	lookup BranchLookup
}

func NewBranchResolver(c cache.Cache, lookup BranchLookup) *CachingBranchResolver {
	return &CachingBranchResolver{cache: c, lookup: lookup}
}

// The key namespace carries the value format. It moved from "observe:branch:"
// when the publish group joined the branch in one entry: during a rolling
// deploy the two versions share the cache, and an old replica reading a new
// entry would take "main\x00<uuid>" for a branch name and write that into an
// append-only table. Different namespace, so each version only ever reads what
// it wrote, at the cost of one re-lookup per live update on the changeover.
func branchCacheKey(appID, updateID string) string {
	return "observe:origin:" + appID + ":" + updateID
}

// Both values live in one cache entry, separated by a NUL: they come from one
// row and neither can change, so caching them apart would double the lookups
// for nothing.
func (r *CachingBranchResolver) UpdateOrigin(ctx context.Context, appID string, updateID string) (string, string) {
	if updateID == "" || updateID == ZeroUpdateID {
		return "", ""
	}
	key := branchCacheKey(appID, updateID)
	if cached := r.cache.Get(key); cached != "" {
		if cached == branchUnknownCacheValue {
			return "", ""
		}
		branch, group, _ := strings.Cut(cached[len(branchKnownValuePrefix):], "\x00")
		return branch, group
	}

	name, group, err := r.lookup(ctx, appID, updateID)
	if err != nil {
		// Transient database trouble: stay uncached so a later batch
		// retries, and let this batch land with an empty branch rather than
		// fail ingestion over an enrichment.
		return "", ""
	}

	value := branchUnknownCacheValue
	if name != "" {
		value = branchKnownValuePrefix + name + "\x00" + group
	}
	ttl := branchCacheTTLSeconds
	// Best-effort: a failed Set only costs a re-lookup on the next batch.
	_ = r.cache.Set(key, value, &ttl)
	return name, group
}
