// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
	"xprem/ee/identity"
	"xprem/internal/cache"
	"xprem/internal/handlers"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deviceCheckIn(deviceID string) handlers.DeviceCheckIn {
	return handlers.DeviceCheckIn{
		AppID:           testAppID,
		EASClientID:     deviceID,
		CurrentUpdateID: testUpdateA,
	}
}

func TestCheckInQueueOverflowDropsAndReleasesClaim(t *testing.T) {
	store := &fakeTouchStore{}
	release := store.hold()
	defer release()
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store), localCache)
	ctx := context.Background()

	// Fill the workers and the whole queue with blocked writes.
	total := checkInWorkerCount + checkInQueueCapacity
	for range total {
		recorder.Record(ctx, deviceCheckIn(uuid.NewString()))
	}
	// The workers pick their jobs up asynchronously; wait until they hold
	// exactly checkInWorkerCount writes so the queue is provably full.
	require.Eventually(t, func() bool {
		return int(store.calls.Load()) == checkInWorkerCount
	}, 2*time.Second, 10*time.Millisecond)

	overflowing := uuid.NewString()
	recorder.Record(ctx, deviceCheckIn(overflowing))

	assert.GreaterOrEqual(t, recorder.dropped.Load(), int64(1), "the overflowing check-in must be counted as dropped")
	taken, err := localCache.TryLock(checkInClaimKey(testAppID, overflowing), 1)
	require.NoError(t, err)
	assert.True(t, taken, "a dropped check-in must release its claim so a later poll can retry")

	// Once the writes unblock, the whole backlog drains, nothing is lost.
	release()
	require.Eventually(t, func() bool {
		return int(store.calls.Load()) == total
	}, 10*time.Second, 20*time.Millisecond)
}

// panicTouchStore panics on the first TouchDevice and behaves afterwards.
type panicTouchStore struct {
	fakeTouchStore
	panicked atomic.Bool
}

func (p *panicTouchStore) TouchDevice(ctx context.Context, appID string, easClientID string, geo *identity.Geo, current *identity.CurrentUpdate, device identity.DeviceInfo) error {
	if p.panicked.CompareAndSwap(false, true) {
		panic("boom")
	}
	return p.fakeTouchStore.TouchDevice(ctx, appID, easClientID, geo, current, device)
}

func TestCheckInWorkerSurvivesPanic(t *testing.T) {
	store := &panicTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store), localCache)
	ctx := context.Background()

	panicking := uuid.NewString()
	recorder.Record(ctx, deviceCheckIn(panicking))
	require.Eventually(t, func() bool {
		return store.panicked.Load()
	}, 2*time.Second, 10*time.Millisecond)

	// The claim of the panicked write must have been released during unwinding.
	require.Eventually(t, func() bool {
		taken, err := localCache.TryLock(checkInClaimKey(testAppID, panicking), 1)
		return err == nil && taken
	}, 2*time.Second, 10*time.Millisecond)

	// And the pool must still process later check-ins.
	survivor := uuid.NewString()
	recorder.Record(ctx, deviceCheckIn(survivor))
	require.Eventually(t, func() bool {
		return localCache.Get(checkInCacheKey(testAppID, survivor)) != ""
	}, 2*time.Second, 10*time.Millisecond)
}
