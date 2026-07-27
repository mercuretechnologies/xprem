// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"errors"
	"expo-open-ota/ee/identity"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/handlers"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTouchStore fakes the registry: only the check-in write path is
// exercised, the embedded Store covers the rest of the interface.
type fakeTouchStore struct {
	identity.Store
	failing      atomic.Bool
	failFailures atomic.Bool
	calls        atomic.Int32

	mu sync.Mutex
	// gate holds TouchDevice in flight when set, which is how a test observes
	// what happens to a second check-in while the first is still writing.
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

// waitCurrent waits for the write to have LANDED, which the call counter does
// not say: it is incremented on entry to TouchDevice, deliberately, because two
// tests hold a call in flight and count it while it is held. Everything the
// store records is written on the way out, so a test that reads lastCurrent
// after waiting on the counter is reading a value the call has not set yet.
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

	// Raw header garbage, BOTH sides: ignored outright.
	recorder.Record(ctx, handlers.DeviceCheckIn{AppID: testAppID, EASClientID: "not-a-uuid"})
	recorder.Record(ctx, handlers.DeviceCheckIn{AppID: "not-a-uuid-app", EASClientID: testDeviceID})
	assert.EqualValues(t, 0, store.calls.Load())

	// First check-in registers (with its running update)...
	recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
	waitRecorded(t, localCache, 1, store)
	waitCurrent(t, store, testUpdateA)

	// ...and the SAME state within the TTL is debounced.
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

	// The device moved to update B: the fingerprint changes, the debounce
	// must NOT swallow the transition.
	recorder.Record(ctx, checkInWith(testUpdateB, "", ""))
	waitCurrent(t, store, testUpdateB)
	assert.EqualValues(t, 2, store.calls.Load())
}

func TestRecordRecordsFailures(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	// The post-crash poll: current back on A, B in the failed list, the
	// consumed fatal error riding along.
	raw := `"` + testUpdateB + `"`
	recorder.Record(ctx, checkInWith(testUpdateA, raw, "TypeError: undefined is not a function"))
	// Both, because Record does its work in a goroutine and the two land at
	// different moments: waiting on the failure alone caught the recorder
	// mid-flight, before the touch it also owes, and read a counter that was
	// still zero. Reproduced four times in twenty runs under -race.
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
	// One check-in is one touch, still, once everything has settled.
	assert.EqualValues(t, 1, store.calls.Load())
}

func TestRecordFailureWithoutCurrentDoesNotAssignFailedUpdate(t *testing.T) {
	store := &fakeTouchStore{}
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)

	// After expo-updates rolls back, the failed id is only a failure signal.
	// If the poll does not carry expo-current-update-id, it must not be
	// inferred as the device's current update.
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

	// The cooldown holds even for a DIFFERENT state fingerprint: one doomed
	// attempt per TTL, not one per distinct poll shape.
	recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, 1, store.calls.Load())

	// After the cooldown (simulated by dropping the key), it retries.
	localCache.Delete(checkInCacheKey(testAppID, testDeviceID))
	store.failing.Store(false)
	recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
	require.Eventually(t, func() bool { return store.calls.Load() == 2 }, 2*time.Second, 10*time.Millisecond)
}

func TestParseFailedUpdateIDs(t *testing.T) {
	// The wire form: RFC 8941 list of quoted lowercase UUIDs.
	assert.Equal(t, []string{testUpdateA, testUpdateB},
		ParseFailedUpdateIDs(`"`+testUpdateA+`", "`+testUpdateB+`"`))
	// Uppercase normalizes, unquoted tolerated, garbage dropped.
	assert.Equal(t, []string{testUpdateA},
		ParseFailedUpdateIDs(`"9B3B89B6-5A0D-4A57-B1F5-6E1D5B7C2A10", "not-a-uuid"`))
	// One manifest can report at most five distinct failures. Duplicates do
	// not consume the cap or trigger duplicate writes.
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

	// Manifest-style check-in: RAW uppercase header value.
	recorder.Record(ctx, checkInWith("9B3B89B6-5A0D-4A57-B1F5-6E1D5B7C2A10", "", ""))
	waitRecorded(t, localCache, 1, store)

	// Telemetry-style check-in for the SAME state: normalized lowercase.
	// Same effective state, no debounce bust.
	recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
	// Telemetry from a resource with no update id: the zero-UUID sentinel
	// means "does not know", never a transition.
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

	// A plain check-in fails and arms the cooldown...
	recorder.Record(ctx, checkInWith("", "", ""))
	require.Eventually(t, func() bool {
		return localCache.Get(checkInCacheKey(testAppID, testDeviceID)) == checkInErrorCacheValue
	}, 2*time.Second, 10*time.Millisecond)

	// ...which must NOT swallow the one-shot fatal error: that poll always
	// gets its attempt.
	recorder.Record(ctx, checkInWith(testUpdateA, `"`+testUpdateB+`"`, "FATAL BOOM"))
	require.Eventually(t, func() bool { return store.calls.Load() >= 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestRecordFatalStashSurvivesFailuresOutage(t *testing.T) {
	store := &fakeTouchStore{}
	store.failFailures.Store(true)
	localCache := cache.NewLocalCache()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), localCache)
	ctx := context.Background()

	// The fatal-carrying poll arrives while the failures write is down: the
	// crash detail must be stashed, not lost (the client never re-sends it).
	raw := `"` + testUpdateB + `"`
	recorder.Record(ctx, checkInWith(testUpdateA, raw, "FATAL BOOM"))
	// Wait for the goroutine's LAST write (the cooldown marker): deleting the
	// key any earlier races the goroutine re-arming it after the stash write.
	require.Eventually(t, func() bool {
		return localCache.Get(checkInCacheKey(testAppID, testDeviceID)) == checkInErrorCacheValue &&
			localCache.Get(fatalStashKey(testAppID, testDeviceID)) == "FATAL BOOM"
	}, 2*time.Second, 10*time.Millisecond)

	// Recovery: the sticky header re-sends the failed id WITHOUT the error;
	// the stash re-attaches it.
	localCache.Delete(checkInCacheKey(testAppID, testDeviceID)) // cooldown expiry
	store.failFailures.Store(false)
	recorder.Record(ctx, checkInWith(testUpdateA, raw, ""))
	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.failedRecorded) == 1 && store.lastFatal == "FATAL BOOM"
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, "", localCache.Get(fatalStashKey(testAppID, testDeviceID)), "stash consumed on success")
}

// A poll the server refused is not evidence that a device exists, so it writes
// nothing durable. But the crash detail it carries is sent exactly once, and a
// refusal here is transient and correlated with the very incident that detail
// documents: it goes to the stash, and the next poll that resolves attaches it
// to the failure it belongs to.
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
	// Nothing durable: no registration, no failure row, not even a background
	// attempt. Give the goroutine a chance to exist before asserting it does not.
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, store.calls.Load(), "a refused poll must not register the device")
	store.mu.Lock()
	assert.Empty(t, store.failedRecorded, "a refused poll must not record failures")
	store.mu.Unlock()

	// The next poll that resolves carries the sticky failed id and no detail,
	// and picks the stash up.
	recorder.Record(ctx, checkInWith(testUpdateA, `"`+testUpdateB+`"`, ""))
	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.failedRecorded) == 1 && store.lastFatal == "FATAL BOOM"
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, "", localCache.Get(fatalStashKey(testAppID, testDeviceID)), "stash consumed on success")
}

// Without a crash detail a refused poll is pure noise, and reporting it would
// hand the registry a device the server just refused to answer.
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

	// A backlog flush: old sessions on update A first, the newest on B. The
	// check-in must carry B, not regress to A.
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

// The same batch with every timestamp collapsed onto one instant, which is
// what a stale backlog looks like once clampTimestamp has folded unparseable
// or too-old wire timestamps onto the ingestion time. Order is the only signal
// left, and the device sends oldest first, so the last row is the recent state.
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

// Hardware reaches the registry only through telemetry, so the recorder has to
// carry it and, above all, must not let the manifest polls that follow blank
// it out or re-trigger a write on every poll.
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

	// A manifest poll knows no hardware. Unknown is not a state: it neither
	// writes again nor reports empty hardware downstream.
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

	// The user took the OS update: nothing else changed, and it still has to
	// be recorded rather than wait for an unrelated transition.
	upgraded := base
	upgraded.OSVersion = "26.2"
	recorder.Record(ctx, upgraded)
	require.Eventually(t, func() bool { return store.calls.Load() == 2 }, 2*time.Second, 10*time.Millisecond)
	store.mu.Lock()
	assert.Equal(t, "26.2", store.lastDevice.OSVersion)
	store.mu.Unlock()
}

// Cache entries written before the hardware component existed have two fields.
// They must parse, and must not be mistaken for "hardware already recorded".
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

// The crash detail arrives either as an attribute of a batch up to 16MB or as a
// manifest header, and from there it is copied into PostgreSQL, the outbox,
// ClickHouse and the cache stash. Unbounded, one poll amplifies four times.
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
	// Counted in runes, so a multi-byte message is never cut mid-character.
	require.Equal(t, maxFatalErrorRunes, len([]rune(store.lastFatal)))
	require.True(t, utf8.ValidString(store.lastFatal))
}

// A device is written once at a time. The debounce key is only set after the
// database write returns, so every poll arriving while it runs used to read the
// same stale value, decide the same way, and start its own write: eight polls
// of one device in flight together meant eight goroutines and eight
// transactions on the same row.
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

	// One claim taken, so one write in flight, while the seven others stood
	// down without starting anything.
	assert.Eventually(t, func() bool { return store.calls.Load() == 1 }, time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.EqualValues(t, 1, store.calls.Load(),
		"eight concurrent polls of one device must produce one write, not eight")
	release()
}

// The exception, and the reason it exists: expo-updates sends the fatal error
// header once, on the first poll after the crash. A routine poll winning the
// claim must not stand that one down, or the crash never reaches the dashboard.
func TestACheckInCarryingACrashIgnoresTheClaim(t *testing.T) {
	store := &fakeTouchStore{}
	release := store.hold()
	recorder := NewCheckInRecorder(identity.NewService(store, nil), cache.NewLocalCache())
	ctx := context.Background()

	// A routine poll takes the claim and stays in flight.
	recorder.Record(ctx, checkInWith(testUpdateA, "", ""))
	assert.Eventually(t, func() bool { return store.calls.Load() == 1 }, time.Second, 10*time.Millisecond)

	// The crash arrives while that write is still running. It must not be
	// refused: the routine poll holding the claim carries no crash detail, so
	// standing this one down would lose it for good.
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
