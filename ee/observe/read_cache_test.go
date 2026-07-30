// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestConcurrentReadsOfOneKeyComputeOnce(t *testing.T) {
	key := readCacheKey("test-collapse", uuid.NewString())
	var computations atomic.Int32

	compute := func(context.Context) (int, error) {
		computations.Add(1)
		time.Sleep(50 * time.Millisecond)
		return 42, nil
	}

	var wg sync.WaitGroup
	answers := make([]int, 10)
	for i := range answers {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			value, err := cachedRead(context.Background(), key, compute)
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

func TestConcurrentReadsOfDifferentKeysDoNotCollapse(t *testing.T) {
	var computations atomic.Int32
	compute := func(context.Context) (int, error) {
		computations.Add(1)
		time.Sleep(20 * time.Millisecond)
		return 1, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cachedRead(context.Background(), readCacheKey("test-distinct", uuid.NewString()), compute)
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	require.EqualValues(t, 4, computations.Load())
}

func TestAFailedReadIsNotCached(t *testing.T) {
	key := readCacheKey("test-failure", uuid.NewString())
	var computations atomic.Int32
	failing := func(context.Context) (int, error) {
		computations.Add(1)
		return 0, errInvalidObserveFilter
	}

	_, err := cachedRead(context.Background(), key, failing)
	require.Error(t, err)
	_, err = cachedRead(context.Background(), key, failing)
	require.Error(t, err)
	require.EqualValues(t, 2, computations.Load(), "a failure must not be served from the cache")
}

func TestALeaderGivingUpDoesNotFailTheReadersBehindIt(t *testing.T) {
	key := readCacheKey("test-detached", uuid.NewString())
	leaderCtx, cancelLeader := context.WithCancel(context.Background())

	started := make(chan struct{})
	var computed atomic.Int32
	compute := func(ctx context.Context) (int, error) {
		computed.Add(1)
		close(started)
		time.Sleep(150 * time.Millisecond)
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return 7, nil
	}

	var wg sync.WaitGroup
	var waiterValue int
	var waiterErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-started
		waiterValue, waiterErr = cachedRead(context.Background(), key, compute)
	}()

	go func() {
		_, _ = cachedRead(leaderCtx, key, compute)
	}()

	<-started
	cancelLeader()
	wg.Wait()

	require.NoError(t, waiterErr, "a reader still connected must get its answer")
	require.Equal(t, 7, waiterValue)
	require.EqualValues(t, 1, computed.Load(), "still one computation for both")
}
