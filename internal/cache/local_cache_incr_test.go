package cache

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIncrCountsFromOne(t *testing.T) {
	c := NewLocalCache()

	for want := int64(1); want <= 3; want++ {
		got, err := c.Incr("auth:login:ip", 60)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}

// The reason Incr is on the interface at all. A counter built out of Get then
// Set loses increments when the callers overlap, and overlapping callers are
// precisely what a rate limiter exists to count: a credential-stuffing run
// arrives in parallel, not one request at a time.
//
// The final assertion is the whole detection mechanism here, and -race is not.
// A read-modify-write that takes the lock for its read and again for its write
// is a lost update, not a data race, so the detector has nothing to report and
// stays silent while every increment but one goes missing. Do not read a clean
// -race run as evidence that this operation is atomic.
//
// The goroutines are released together rather than spawned in a loop so they
// genuinely overlap. Spawning alone, the early ones finish before the later
// ones start often enough that a lost-update implementation slips through a
// few percent of runs, and on a single-processor run it slips through always.
func TestIncrIsAtomicUnderConcurrency(t *testing.T) {
	c := NewLocalCache()

	const attempts = 200
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	for range attempts {
		ready.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			_, _ = c.Incr("auth:login:ip", 60)
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()

	final, err := c.Incr("auth:login:ip", 60)
	require.NoError(t, err)
	require.Equal(t, int64(attempts+1), final)
}

// The window starts at the first attempt and ends on schedule. If every
// increment pushed the expiry back, an attacker hammering an account would
// keep the block alive indefinitely and the legitimate owner would never get
// back in, which turns the rate limiter into the denial of service.
func TestIncrDoesNotExtendTheWindow(t *testing.T) {
	c := NewLocalCache()

	_, err := c.Incr("auth:login:account", 60)
	require.NoError(t, err)

	c.mu.RLock()
	firstExpiry := *c.items[withPrefix("auth:login:account")].Expiration
	c.mu.RUnlock()

	time.Sleep(5 * time.Millisecond)
	_, err = c.Incr("auth:login:account", 60)
	require.NoError(t, err)

	c.mu.RLock()
	secondExpiry := *c.items[withPrefix("auth:login:account")].Expiration
	c.mu.RUnlock()

	require.True(t, firstExpiry.Equal(secondExpiry), "the second increment moved the expiry from %s to %s", firstExpiry, secondExpiry)
}

// Once the window has passed the subject gets a clean slate, the same way the
// Redis key would simply have been evicted.
//
// Restarting at 1 is only half of it, which is why the count is asserted twice.
// A restart that reuses the stale deadline instead of opening a fresh window
// also returns 1, and then returns 1 to every call after it: the counter is
// pinned below the limit for good and the subject can never be throttled again.
// That reads as correct from the first increment alone, so the second is what
// actually distinguishes a new window from a dead one.
func TestIncrRestartsAfterTheWindowPasses(t *testing.T) {
	c := NewLocalCache()

	_, err := c.Incr("auth:login:account", 60)
	require.NoError(t, err)

	past := time.Now().Add(-time.Minute)
	c.mu.Lock()
	c.items[withPrefix("auth:login:account")] = CacheItem{Value: "9", Expiration: &past}
	c.mu.Unlock()

	got, err := c.Incr("auth:login:account", 60)
	require.NoError(t, err)
	require.Equal(t, int64(1), got)

	got, err = c.Incr("auth:login:account", 60)
	require.NoError(t, err)
	require.Equal(t, int64(2), got, "the new window is not counting, the counter is stuck at 1")
}

// Counters must not bleed into each other. A limiter keys one counter per
// account and per IP, so a keying bug has only two outcomes, both total: every
// subject shares one counter and a single attacker locks out the whole
// instance, or the key is constant per subject and nobody is ever limited.
func TestIncrKeepsCountersSeparatePerKey(t *testing.T) {
	c := NewLocalCache()

	for range 3 {
		_, err := c.Incr("auth:login:ip:198.51.100.1", 60)
		require.NoError(t, err)
	}

	other, err := c.Incr("auth:login:ip:203.0.113.9", 60)
	require.NoError(t, err)
	require.Equal(t, int64(1), other)

	first, err := c.Incr("auth:login:ip:198.51.100.1", 60)
	require.NoError(t, err)
	require.Equal(t, int64(4), first)
}

// A counter must always carry a deadline, even when it inherits a key that was
// written without one. sweepLocked keeps nil-Expiration entries on purpose, so
// a counter that reached the increment path with no TTL would grow forever and
// its subject would never be un-blocked: waiting would not help, only a restart
// or an explicit Delete would.
//
// The seed is deliberately not "0". Zero is the one value an implementation
// that arms the deadline on "the counter reads 1" rather than on "the counter
// has no deadline" still handles correctly, so seeding it would let such an
// implementation pass. That distinction is not academic: the Redis backend was
// written the first way, and this is the value that separates the two.
func TestIncrGivesADeadlineToACounterThatHasNone(t *testing.T) {
	c := NewLocalCache()
	require.NoError(t, c.Set("auth:login:ip", "5", nil))

	got, err := c.Incr("auth:login:ip", 60)
	require.NoError(t, err)
	require.Equal(t, int64(6), got)

	c.mu.RLock()
	expiration := c.items[withPrefix("auth:login:ip")].Expiration
	c.mu.RUnlock()
	require.NotNil(t, expiration, "the counter would never expire")
}

// Redis answers INCR on a non-numeric key with an error rather than resetting
// it, and so must this: silently overwriting would destroy whatever the other
// caller stored under that key.
func TestIncrRefusesANonNumericValue(t *testing.T) {
	c := NewLocalCache()
	ttl := 60
	require.NoError(t, c.Set("occupied", "not-a-number", &ttl))

	_, err := c.Incr("occupied", 60)
	require.Error(t, err)
	require.Equal(t, "not-a-number", c.Get("occupied"))
}
