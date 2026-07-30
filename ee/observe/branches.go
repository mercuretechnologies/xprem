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

// BranchLookup resolves an update to its branch and publish group.
// ("", "", nil) means "no such update", permanent and cacheable; an error is transient.
type BranchLookup func(ctx context.Context, appID string, updateUUID string) (branch string, updateGroupID string, err error)

const branchCacheTTLSeconds = 3600

const (
	branchKnownValuePrefix  = "b"
	branchUnknownCacheValue = "0"
)

// CachingBranchResolver memoizes lookups in the process cache. Negative
// results are cached too, since update ids are never recycled.
type CachingBranchResolver struct {
	cache  cache.Cache
	lookup BranchLookup
}

func NewBranchResolver(c cache.Cache, lookup BranchLookup) *CachingBranchResolver {
	return &CachingBranchResolver{cache: c, lookup: lookup}
}

func branchCacheKey(appID, updateID string) string {
	return "observe:origin:" + appID + ":" + updateID
}

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
		return "", ""
	}

	value := branchUnknownCacheValue
	if name != "" {
		value = branchKnownValuePrefix + name + "\x00" + group
	}
	ttl := branchCacheTTLSeconds
	_ = r.cache.Set(key, value, &ttl)
	return name, group
}
