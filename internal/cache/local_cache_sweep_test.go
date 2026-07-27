package cache

import (
	"strconv"
	"testing"
	"time"
)

// An expired key used to be freed only by a Get on that same key, so anything
// written once and never read again stayed for the life of the process. That
// is the shape of most of what this cache holds: unknown app ids, unknown
// update ids, stashed crash details, one entry per device on a large fleet.
// Every one of them is written far more often than it is read back.
func TestExpiredKeysAreFreedWithoutBeingRead(t *testing.T) {
	c := NewLocalCache()
	ttl := 1
	for i := 0; i < 500; i++ {
		if err := c.Set("observe:app_exists:"+strconv.Itoa(i), "0", &ttl); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if got := len(c.items); got != 500 {
		t.Fatalf("expected the writes to land, got %d entries", got)
	}

	// Past their TTL, and past the sweep interval, without a single Get.
	c.mu.Lock()
	c.lastSweep = time.Now().Add(-2 * sweepInterval)
	for key, item := range c.items {
		past := time.Now().Add(-time.Minute)
		c.items[key] = CacheItem{Value: item.Value, Expiration: &past}
	}
	c.mu.Unlock()

	// One more write is all it takes: the map only grows through Set, so that
	// is where the sweep belongs.
	if err := c.Set("observe:app_exists:trigger", "1", &ttl); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := len(c.items); got != 1 {
		t.Fatalf("expired entries must be gone, %d survived", got)
	}
}

// A sweep must not touch what has no TTL: those entries were written to live
// until replaced or deleted, and dropping them would be a cache miss nobody
// asked for.
func TestSweepKeepsEntriesWithoutTTL(t *testing.T) {
	c := NewLocalCache()
	if err := c.Set("permanent", "value", nil); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ttl := 1
	expired := time.Now().Add(-time.Minute)
	c.mu.Lock()
	c.lastSweep = time.Now().Add(-2 * sweepInterval)
	c.items[withPrefix("transient")] = CacheItem{Value: "v", Expiration: &expired}
	c.mu.Unlock()

	if err := c.Set("trigger", "1", &ttl); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if c.Get("permanent") != "value" {
		t.Fatal("an entry with no TTL must survive a sweep")
	}
	if c.Get("transient") != "" {
		t.Fatal("an expired entry must not survive a sweep")
	}
}

// Sets carry their own expirations, in a second pair of maps. They expire the
// same way and were leaking the same way.
func TestSweepFreesExpiredSets(t *testing.T) {
	c := NewLocalCache()
	ttl := 1
	if err := c.Sadd("devices", []string{"a", "b"}, &ttl); err != nil {
		t.Fatalf("Sadd: %v", err)
	}
	past := time.Now().Add(-time.Minute)
	c.mu.Lock()
	c.lastSweep = time.Now().Add(-2 * sweepInterval)
	c.setExpirations[withPrefix("devices")] = &past
	c.mu.Unlock()

	if err := c.Set("trigger", "1", &ttl); err != nil {
		t.Fatalf("Set: %v", err)
	}
	c.mu.RLock()
	items, expirations := len(c.setItems), len(c.setExpirations)
	c.mu.RUnlock()
	if items != 0 || expirations != 0 {
		t.Fatalf("an expired set must be freed, %d sets and %d expirations left", items, expirations)
	}
}

// The sweep is amortised: it costs one map walk per interval, not one per Set.
func TestSweepIsRateLimited(t *testing.T) {
	c := NewLocalCache()
	ttl := 1
	if err := c.Set("first", "1", &ttl); err != nil {
		t.Fatalf("Set: %v", err)
	}
	past := time.Now().Add(-time.Minute)
	c.mu.Lock()
	c.items[withPrefix("first")] = CacheItem{Value: "1", Expiration: &past}
	before := c.lastSweep
	c.mu.Unlock()

	if err := c.Set("second", "2", &ttl); err != nil {
		t.Fatalf("Set: %v", err)
	}
	c.mu.RLock()
	swept := !c.lastSweep.Equal(before)
	_, expiredSurvives := c.items[withPrefix("first")]
	c.mu.RUnlock()
	if swept {
		t.Fatal("a Set within the interval must not pay for a sweep")
	}
	if !expiredSurvives {
		t.Fatal("without a sweep the expired entry is simply still there, freed by its own Get")
	}
}
