// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
	"xprem/ee/identity"
	"xprem/internal/cache"
	"xprem/internal/handlers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTouchStore fakes the registry's check-in write path.
type fakeTouchStore struct {
	identity.Store
	failing      atomic.Bool
	failFailures atomic.Bool
	calls        atomic.Int32

	mu sync.Mutex
	// gate holds TouchDevice in flight so a test can observe a second check-in mid-write.
	gate           chan struct{}
	lastCurrent    *identity.CurrentUpdate
	lastDevice     identity.DeviceInfo
	failedRecorded [][]string
	lastFatal      string
	lastType       identity.FailureType
}

func (f *fakeTouchStore) TouchDevice(_ context.Context, _ string, _ string, _ *identity.Geo, current *identity.CurrentUpdate, device identity.DeviceInfo) error {
	f.calls.Add(1)
	f.mu.Lock()
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if f.failing.Load() {
		return errors.New("connection refused")
	}
	f.mu.Lock()
	f.lastCurrent = current
	f.lastDevice = device
	f.mu.Unlock()
	return nil
}

// hold makes every TouchDevice wait until the returned func is called.
func (f *fakeTouchStore) hold() func() {
	gate := make(chan struct{})
	f.mu.Lock()
	f.gate = gate
	f.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			f.gate = nil
			f.mu.Unlock()
			close(gate)
		})
	}
}

func (f *fakeTouchStore) RecordUpdateFailures(_ context.Context, _ string, _ string, updateIDs []string, fatalError string, failureType identity.FailureType) error {
	if f.failFailures.Load() {
		return errors.New("failures write refused")
	}
	f.mu.Lock()
	f.failedRecorded = append(f.failedRecorded, updateIDs)
	f.lastFatal = fatalError
	f.lastType = failureType
	f.mu.Unlock()
	return nil
}

const (
	testAppID    = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	testDeviceID = "3f9b2c81-4a5d-4e6f-8a9b-0c1d2e3f4a5b"
	testUpdateA  = "9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10"
	testUpdateB  = "0f61f1d1-3f5f-4b6a-9a44-6e9a1c2b3d4e"
)

func checkInWith(current, failedRaw, fatal string) handlers.DeviceCheckIn {
	return handlers.DeviceCheckIn{
		AppID:              testAppID,
		EASClientID:        testDeviceID,
		CurrentUpdateID:    current,
		FailedUpdateIDsRaw: failedRaw,
		FatalError:         fatal,
	}
}

func waitRecorded(t *testing.T, c cache.Cache, want int32, store *fakeTouchStore) {
	t.Helper()
	require.Eventually(t, func() bool {
		return c.Get(checkInCacheKey(testAppID, testDeviceID)) != "" && store.calls.Load() == want
	}, 2*time.Second, 10*time.Millisecond)
}

// waitCurrent waits for the write to have landed; the call counter alone
// does not say that, since it increments on entry to TouchDevice.
func waitCurrent(t *testing.T, store *fakeTouchStore, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.lastCurrent != nil && store.lastCurrent.ID == want
	}, 2*time.Second, 10*time.Millisecond)
}

func TestRecordDebouncesSteadyState(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	recorder.Record(ctx, handlers.DeviceCheckIn{AppID: testAppID, EASClientID: "not-a-uuid"})
	recorder.Record(ctx, handlers.DeviceCheckIn{AppID: "not-a-uuid-app", EASClientID: testDeviceID})
	assert.EqualValues(t, 0, store.calls.Load())

	recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
	waitRecorded(t, localCache, 1, store)
	waitCurrent(t, store, testUpdateA)

	recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
	time.Sleep(50 * time.Millisecond)
	assert.EqualValues(t, 1, store.calls.Load())
}

func TestRecordStateTransitionBustsDebounce(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
	waitRecorded(t, localCache, 1, store)

	recorder.Record(ctx, checkInWith(testUpdateB, "", ""))
	waitCurrent(t, store, testUpdateB)
	assert.EqualValues(t, 2, store.calls.Load())
}

func TestRecordRecordsFailures(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	raw := `"` + testUpdateB + `"`
	recorder.Record(ctx, checkInWith(testUpdateA, raw, "TypeError: undefined is not a function"))
	// Wait on both counters: Record's write and touch land at different moments.
	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.failedRecorded) == 1 && store.calls.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
	store.mu.Lock()
	assert.Equal(t, []string{testUpdateB}, store.failedRecorded[0])
	assert.Equal(t, "TypeError: undefined is not a function", store.lastFatal)
	assert.Equal(t, identity.FailureTypeUpdate, store.lastType)
	store.mu.Unlock()
	assert.EqualValues(t, 1, store.calls.Load())
}

func TestRecordFailureWithoutCurrentDoesNotAssignFailedUpdate(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)

	recorder.Record(context.Background(), checkInWith("", `"`+testUpdateB+`"`, "launch failed"))
	waitRecorded(t, localCache, 1, store)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Nil(t, store.lastCurrent)
	require.Equal(t, [][]string{{testUpdateB}}, store.failedRecorded)
}

func TestRecordErrorCooldown(t *testing.T) {
	store := &fakeTouchStore{}
	store.failing.Store(true)
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	recorder.Record(ctx, checkInWith("", "", ""))
	require.Eventually(t, func() bool {
		return localCache.Get(checkInCacheKey(testAppID, testDeviceID)) == checkInErrorCacheValue
	}, 2*time.Second, 10*time.Millisecond)

	recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, 1, store.calls.Load())

	localCache.Delete(checkInCacheKey(testAppID, testDeviceID))
	store.failing.Store(false)
	recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
	require.Eventually(t, func() bool { return store.calls.Load() == 2 }, 2*time.Second, 10*time.Millisecond)
}

func TestParseFailedUpdateIDs(t *testing.T) {
	assert.Equal(t, []string{testUpdateA, testUpdateB},
		ParseFailedUpdateIDs(`"`+testUpdateA+`", "`+testUpdateB+`"`))
	assert.Equal(t, []string{testUpdateA},
		ParseFailedUpdateIDs(`"9B3B89B6-5A0D-4A57-B1F5-6E1D5B7C2A10", "not-a-uuid"`))
	// At most five distinct failures; duplicates do not consume the cap.
	extra := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
	}
	raw := `"` + testUpdateA + `", "` + testUpdateA + `", "` +
		strings.Join(extra, `", "`) + `", "` + testUpdateB + `"`
	assert.Equal(t, append([]string{testUpdateA}, extra...),
		ParseFailedUpdateIDs(raw))
	assert.Nil(t, ParseFailedUpdateIDs(""))
	assert.Nil(t, ParseFailedUpdateIDs(`totally, broken, garbage`))
}

func TestRecordCrossSourceEquivalence(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	recorder.Record(ctx, checkInWith("9B3B89B6-5A0D-4A57-B1F5-6E1D5B7C2A10", "", ""))
	waitRecorded(t, localCache, 1, store)

	recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
	recorder.Record(ctx, checkInWith(ZeroUpdateID, "", ""))
	time.Sleep(50 * time.Millisecond)
	assert.EqualValues(t, 1, store.calls.Load(), "same effective state across sources must stay debounced")
}

func TestRecordFatalBypassesCooldown(t *testing.T) {
	store := &fakeTouchStore{}
	store.failing.Store(true)
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	recorder.Record(ctx, checkInWith("", "", ""))
	require.Eventually(t, func() bool {
		return localCache.Get(checkInCacheKey(testAppID, testDeviceID)) == checkInErrorCacheValue
	}, 2*time.Second, 10*time.Millisecond)

	recorder.Record(ctx, checkInWith(testUpdateA, `"`+testUpdateB+`"`, "FATAL BOOM"))
	require.Eventually(t, func() bool { return store.calls.Load() >= 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestRecordFatalStashSurvivesFailuresOutage(t *testing.T) {
	store := &fakeTouchStore{}
	store.failFailures.Store(true)
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	raw := `"` + testUpdateB + `"`
	recorder.Record(ctx, checkInWith(testUpdateA, raw, "FATAL BOOM"))
	// Wait for the goroutine's last write, the cooldown marker, to avoid a race with its re-arming.
	require.Eventually(t, func() bool {
		return localCache.Get(checkInCacheKey(testAppID, testDeviceID)) == checkInErrorCacheValue &&
			localCache.Get(fatalStashKey(testAppID, testDeviceID)) == "FATAL BOOM"
	}, 2*time.Second, 10*time.Millisecond)

	localCache.Delete(checkInCacheKey(testAppID, testDeviceID))
	store.failFailures.Store(false)
	recorder.Record(ctx, checkInWith(testUpdateA, raw, ""))
	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.failedRecorded) == 1 && store.lastFatal == "FATAL BOOM"
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, "", localCache.Get(fatalStashKey(testAppID, testDeviceID)), "stash consumed on success")
}

func TestRecordRejectedPollStashesOnlyTheCrashDetail(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	rejected := checkInWith(testUpdateA, `"`+testUpdateB+`"`, "FATAL BOOM")
	rejected.Rejected = true
	recorder.Record(ctx, rejected)

	require.Equal(t, "FATAL BOOM", localCache.Get(fatalStashKey(testAppID, testDeviceID)))
	assert.Equal(t, "", localCache.Get(checkInCacheKey(testAppID, testDeviceID)),
		"a refused poll must not arm the debounce either")
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, store.calls.Load(), "a refused poll must not register the device")
	store.mu.Lock()
	assert.Empty(t, store.failedRecorded, "a refused poll must not record failures")
	store.mu.Unlock()

	recorder.Record(ctx, checkInWith(testUpdateA, `"`+testUpdateB+`"`, ""))
	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.failedRecorded) == 1 && store.lastFatal == "FATAL BOOM"
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, "", localCache.Get(fatalStashKey(testAppID, testDeviceID)), "stash consumed on success")
}

func TestRecordRejectedPollWithoutCrashDetailIsIgnored(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)

	rejected := checkInWith(testUpdateA, `"`+testUpdateB+`"`, "")
	rejected.Rejected = true
	recorder.Record(context.Background(), rejected)

	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, store.calls.Load())
	assert.Equal(t, "", localCache.Get(fatalStashKey(testAppID, testDeviceID)))
}

func TestRecordsPicksNewestRowPerDevice(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	now := time.Now()
	rows := []LogRow{
		{Envelope: Envelope{EASClientID: testDeviceID, UpdateID: testUpdateA, Timestamp: now.Add(-2 * time.Hour)}},
		{Envelope: Envelope{EASClientID: testDeviceID, UpdateID: testUpdateA, Timestamp: now.Add(-1 * time.Hour)}},
		{Envelope: Envelope{EASClientID: testDeviceID, UpdateID: testUpdateB, Timestamp: now}},
	}
	recordCheckIns(ctx, recorder, testAppID, "", rows,
		func(row LogRow) Envelope { return row.Envelope })
	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.lastCurrent != nil && store.lastCurrent.ID == testUpdateB
	}, 2*time.Second, 10*time.Millisecond)
	assert.EqualValues(t, 1, store.calls.Load(), "one check-in per device per batch")
}

// Timestamps tied (e.g. clamped): order is the only signal, and the device sends oldest first.
func TestRecordsPicksTheLastRowWhenTimestampsTie(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	clamped := time.Now()
	rows := []LogRow{
		{Envelope: Envelope{EASClientID: testDeviceID, UpdateID: testUpdateA, Timestamp: clamped}},
		{Envelope: Envelope{EASClientID: testDeviceID, UpdateID: testUpdateA, Timestamp: clamped}},
		{Envelope: Envelope{EASClientID: testDeviceID, UpdateID: testUpdateB, Timestamp: clamped}},
	}
	recordCheckIns(ctx, recorder, testAppID, "", rows,
		func(row LogRow) Envelope { return row.Envelope })

	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.lastCurrent != nil
	}, 2*time.Second, 10*time.Millisecond)
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, testUpdateB, store.lastCurrent.ID,
		"tied timestamps must not hand the registry the update the device already left")
}

func TestRecordCarriesReportedHardware(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	recorder.Record(ctx, handlers.DeviceCheckIn{
		AppID:           testAppID,
		EASClientID:     testDeviceID,
		CurrentUpdateID: testUpdateA,
		DeviceModel:     "iPhone18,2",
		OSName:          "iOS",
		OSVersion:       "26.1",
	})
	waitRecorded(t, localCache, 1, store)
	store.mu.Lock()
	assert.Equal(t, identity.DeviceInfo{Model: "iPhone18,2", OSName: "iOS", OSVersion: "26.1"}, store.lastDevice)
	store.mu.Unlock()

	recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
	time.Sleep(50 * time.Millisecond)
	assert.EqualValues(t, 1, store.calls.Load())
}

func TestRecordWritesThroughHardwareChange(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	base := handlers.DeviceCheckIn{
		AppID:           testAppID,
		EASClientID:     testDeviceID,
		CurrentUpdateID: testUpdateA,
		DeviceModel:     "iPhone18,2",
		OSName:          "iOS",
		OSVersion:       "26.1",
	}
	recorder.Record(ctx, base)
	waitRecorded(t, localCache, 1, store)

	upgraded := base
	upgraded.OSVersion = "26.2"
	recorder.Record(ctx, upgraded)
	require.Eventually(t, func() bool { return store.calls.Load() == 2 }, 2*time.Second, 10*time.Millisecond)
	store.mu.Lock()
	assert.Equal(t, "26.2", store.lastDevice.OSVersion)
	store.mu.Unlock()
}

func TestParseCachedCheckInAcceptsLegacyValue(t *testing.T) {
	current, failedFP, deviceFP, ok := parseCachedCheckIn("f:" + testUpdateA + ":fabc")
	require.True(t, ok)
	assert.Equal(t, testUpdateA, current)
	assert.Equal(t, "fabc", failedFP)
	assert.Empty(t, deviceFP)

	current, failedFP, deviceFP, ok = parseCachedCheckIn(cachedCheckInValue(testUpdateA, "fabc", "d42"))
	require.True(t, ok)
	assert.Equal(t, testUpdateA, current)
	assert.Equal(t, "fabc", failedFP)
	assert.Equal(t, "d42", deviceFP)
}

func TestFatalErrorIsBoundedBeforeItIsStoredAnywhere(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)

	huge := strings.Repeat("é", maxFatalErrorRunes*3)
	recorder.Record(context.Background(), checkInWith(testUpdateA, `"`+testUpdateB+`"`, huge))

	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.lastFatal != ""
	}, 2*time.Second, 10*time.Millisecond)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Equal(t, maxFatalErrorRunes, len([]rune(store.lastFatal)))
	require.True(t, utf8.ValidString(store.lastFatal))
}

func TestConcurrentCheckInsOfOneDeviceCollapseToOneWrite(t *testing.T) {
	store := &fakeTouchStore{}
	release := store.hold()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), cache.NewLocalCache())
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
		}()
	}
	wg.Wait()

	assert.Eventually(t, func() bool { return store.calls.Load() == 1 }, time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.EqualValues(t, 1, store.calls.Load(),
		"eight concurrent polls of one device must produce one write, not eight")
	release()
}

func TestACheckInCarryingACrashIgnoresTheClaim(t *testing.T) {
	store := &fakeTouchStore{}
	release := store.hold()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), cache.NewLocalCache())
	ctx := context.Background()

	recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
	assert.Eventually(t, func() bool { return store.calls.Load() == 1 }, time.Second, 10*time.Millisecond)

	recorder.Record(ctx, checkInWith(testUpdateA, testUpdateB, "TypeError: undefined is not a function"))
	assert.Eventually(t, func() bool { return store.calls.Load() == 2 }, time.Second, 10*time.Millisecond,
		"a poll carrying a crash must never be refused by the claim")

	release()
	assert.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.lastFatal == "TypeError: undefined is not a function"
	}, time.Second, 10*time.Millisecond, "the crash detail must reach the store")
}
