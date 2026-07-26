// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"errors"
	"expo-open-ota/internal/cache"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBranchResolverCachesPositiveAndNegative(t *testing.T) {
	calls := 0
	resolver := NewBranchResolver(cache.NewLocalCache(), func(_ context.Context, _, updateUUID string) (string, string, error) {
		calls++
		if updateUUID == "9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10" {
			return "main", "3f7c1d64-1a2b-4c3d-8e9f-0a1b2c3d4e5f", nil
		}
		return "", "", nil // unknown update: permanent absence
	})
	ctx := context.Background()

	branch, group := resolver.UpdateOrigin(ctx, "app-1", "9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10")
	assert.Equal(t, "main", branch)
	assert.Equal(t, "3f7c1d64-1a2b-4c3d-8e9f-0a1b2c3d4e5f", group)
	// Both come from one row and neither can change, so one entry caches both.
	branch, group = resolver.UpdateOrigin(ctx, "app-1", "9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10")
	assert.Equal(t, "main", branch)
	assert.Equal(t, "3f7c1d64-1a2b-4c3d-8e9f-0a1b2c3d4e5f", group)
	assert.Equal(t, 1, calls, "positive result cached after the first lookup")

	branch, group = resolver.UpdateOrigin(ctx, "app-1", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	assert.Empty(t, branch)
	assert.Empty(t, group)
	_, _ = resolver.UpdateOrigin(ctx, "app-1", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	assert.Equal(t, 2, calls, "negative result cached too: update ids are never recycled")
}

func TestBranchResolverDoesNotCacheTransientErrors(t *testing.T) {
	calls := 0
	broken := true
	resolver := NewBranchResolver(cache.NewLocalCache(), func(_ context.Context, _, _ string) (string, string, error) {
		calls++
		if broken {
			return "", "", errors.New("connection refused")
		}
		return "main", "", nil
	})
	ctx := context.Background()

	// While the database is down the batch lands with an empty branch...
	branch, _ := resolver.UpdateOrigin(ctx, "app-1", "9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10")
	assert.Empty(t, branch)
	// ...and recovery is picked up by the next batch, not poisoned by a cache.
	broken = false
	branch, _ = resolver.UpdateOrigin(ctx, "app-1", "9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10")
	assert.Equal(t, "main", branch)
	assert.Equal(t, 2, calls)
}

func TestBranchResolverShortCircuitsEmbeddedBundle(t *testing.T) {
	resolver := NewBranchResolver(cache.NewLocalCache(), func(_ context.Context, _, _ string) (string, string, error) {
		t.Fatal("the zero update id must never reach the lookup")
		return "", "", nil
	})
	branch, group := resolver.UpdateOrigin(context.Background(), "app-1", ZeroUpdateID)
	assert.Empty(t, branch)
	assert.Empty(t, group)
	branch, group = resolver.UpdateOrigin(context.Background(), "app-1", "")
	assert.Empty(t, branch)
	assert.Empty(t, group)
}
