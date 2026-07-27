// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The cache collapses readers that arrive one after another. This is about the
// ones that arrive together, which is the case that actually happens: the
// dashboard refetches on a timer, so every open tab misses the same key within
// milliseconds of the entry expiring. Without this, ten tabs meant ten
// simultaneous aggregations of the same rows, and the cache only ever saw the
// last one.
func TestConcurrentReadsOfOneKeyComputeOnce(t *testing.T) {
	key := readCacheKey("test-collapse", uuid.NewString())
	var computations atomic.Int32

	compute := func() (int, error) {
		computations.Add(1)
		// Long enough that the others are certainly inside cachedRead while
		// this one runs, which is what makes the assertion mean something.
		time.Sleep(50 * time.Millisecond)
		return 42, nil
	}

	var wg sync.WaitGroup
	answers := make([]int, 10)
	for i := range answers {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			value, err := cachedRead(key, compute)
			require.NoError(t, err)
			answers[slot] = value
		}(i)
	}
	wg.Wait()

	require.EqualValues(t, 1, computations.Load(),
		"ten simultaneous readers of one key must cost one aggregation")
	for _, answer := range answers {
		require.Equal(t, 42, answer, "every reader gets the answer, not just the one that computed it")
	}
}

// Two different questions must still be two computations: collapsing is keyed
// on the fingerprint, so it must not merge readers who would not have shared
// the answer either.
func TestConcurrentReadsOfDifferentKeysDoNotCollapse(t *testing.T) {
	var computations atomic.Int32
	compute := func() (int, error) {
		computations.Add(1)
		time.Sleep(20 * time.Millisecond)
		return 1, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cachedRead(readCacheKey("test-distinct", uuid.NewString()), compute)
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	require.EqualValues(t, 4, computations.Load())
}

// A failed computation must not be cached, and must reach every caller waiting
// on it. Caching the failure would hold a stale error for the whole TTL over
// something as transient as a ClickHouse blip.
func TestAFailedReadIsNotCached(t *testing.T) {
	key := readCacheKey("test-failure", uuid.NewString())
	var computations atomic.Int32
	failing := func() (int, error) {
		computations.Add(1)
		return 0, errInvalidObserveFilter
	}

	_, err := cachedRead(key, failing)
	require.Error(t, err)
	_, err = cachedRead(key, failing)
	require.Error(t, err)
	require.EqualValues(t, 2, computations.Load(), "a failure must not be served from the cache")
}
