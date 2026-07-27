package cache

import (
	"expo-open-ota/internal/version"
	"fmt"
	"sync"
	"time"
)

// sweepInterval bounds how often a Set pays for a sweep. A minute is short
// enough that a burst of one-minute keys does not outlive its usefulness by
// much, and long enough that the sweep is amortised to nothing: one map walk
// per minute against however many Sets happened in it.
const sweepInterval = time.Minute

type LocalCache struct {
	items          map[string]CacheItem
	setItems       map[string]map[string]struct{}
	setExpirations map[string]*time.Time
	// lastSweep is when expired entries were last dropped. Without it an
	// expired key is only ever freed by a Get on that same key, so anything
	// written once and never read again stays for the life of the process:
	// unknown app ids, unknown update ids, stashed crash details, and one
	// entry per device on a fleet of a million. Every one of those is written
	// far more often than it is re-read.
	//
	// Swept from Set rather than from a goroutine on purpose. The map only
	// grows through Set, so a cache nobody writes to needs no sweeping at all,
	// and this way there is no ticker to stop, no lifecycle to own and nothing
	// leaked by the many short-lived caches the tests build.
	lastSweep time.Time
	mu        sync.RWMutex // RWMutex for safe concurrent access
}

type CacheItem struct {
	Value      string
	Expiration *time.Time // nil if no TTL
}

func NewLocalCache() *LocalCache {
	return &LocalCache{
		items:          make(map[string]CacheItem),
		setItems:       make(map[string]map[string]struct{}),
		setExpirations: make(map[string]*time.Time),
		lastSweep:      time.Now(),
	}
}

// sweepLocked drops every entry whose TTL has passed. The caller holds the
// write lock. Entries with no TTL are kept: they were written to live until
// they are replaced or deleted, and dropping them here would be a cache miss
// nobody asked for.
// maybeSweepLocked sweeps if the interval has passed. Called from every write
// path, because every write path is a way for the maps to grow: Set feeds
// items, Sadd feeds setItems and setExpirations. Sweeping from only one of
// them would leave the other's growth resting on the hope that the first is
// also being called, and the metrics path is a counter-example: TrackActiveUser
// only ever calls Sadd and Scard.
func (c *LocalCache) maybeSweepLocked(now time.Time) {
	if now.Sub(c.lastSweep) >= sweepInterval {
		c.sweepLocked(now)
	}
}

func (c *LocalCache) sweepLocked(now time.Time) {
	c.lastSweep = now
	for key, item := range c.items {
		if item.Expiration != nil && now.After(*item.Expiration) {
			delete(c.items, key)
		}
	}
	for key, expiration := range c.setExpirations {
		if expiration != nil && now.After(*expiration) {
			delete(c.setExpirations, key)
			delete(c.setItems, key)
		}
	}
}

func (c *LocalCache) Get(key string) string {
	c.mu.RLock()
	item, exists := c.items[withPrefix(key)]
	c.mu.RUnlock()
	if !exists {
		return ""
	}

	if item.Expiration != nil && time.Now().After(*item.Expiration) {
		// Deleting under the read lock would be a concurrent map write (two
		// Gets racing on an expired key is an unrecoverable runtime fatal):
		// upgrade to the write lock and re-check, a concurrent Set may have
		// refreshed the entry in the gap.
		c.mu.Lock()
		if current, ok := c.items[withPrefix(key)]; ok && current.Expiration != nil && time.Now().After(*current.Expiration) {
			delete(c.items, withPrefix(key))
		}
		c.mu.Unlock()
		return ""
	}

	return item.Value
}

func (c *LocalCache) Set(key string, value string, ttl *int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var expiration *time.Time
	if ttl != nil {
		exp := time.Now().Add(time.Duration(*ttl) * time.Second)
		expiration = &exp
	}

	c.items[withPrefix(key)] = CacheItem{
		Value:      value,
		Expiration: expiration,
	}

	c.maybeSweepLocked(time.Now())
	return nil
}

func (c *LocalCache) Delete(key string) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.items, withPrefix(key))
}

func (c *LocalCache) Clear() error {
	if version.Version != "development" {
		fmt.Println("Cache can only be cleared in development mode.")
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]CacheItem)
	return nil
}

func (c *LocalCache) TryLock(key string, ttl int) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.items[withPrefix(key)]; exists {
		return false, nil
	}

	exp := time.Now().Add(time.Duration(ttl) * time.Second)
	c.items[withPrefix(key)] = CacheItem{
		Value:      "locked",
		Expiration: &exp,
	}

	go func() {
		time.Sleep(time.Duration(ttl) * time.Second)
		c.mu.Lock()
		delete(c.items, withPrefix(key))
		c.mu.Unlock()
	}()

	return true, nil
}

func (c *LocalCache) Sadd(key string, members []string, ttl *int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	prefixedKey := withPrefix(key)

	if _, exists := c.setItems[prefixedKey]; !exists {
		c.setItems[prefixedKey] = make(map[string]struct{})
		if ttl != nil {
			exp := time.Now().Add(time.Duration(*ttl) * time.Second)
			c.setExpirations[prefixedKey] = &exp
		}
	}

	if exp, ok := c.setExpirations[prefixedKey]; ok && time.Now().After(*exp) {
		delete(c.setItems, prefixedKey)
		delete(c.setExpirations, prefixedKey)
		c.setItems[prefixedKey] = make(map[string]struct{})
		if ttl != nil {
			exp := time.Now().Add(time.Duration(*ttl) * time.Second)
			c.setExpirations[prefixedKey] = &exp
		}
	}

	for _, member := range members {
		c.setItems[prefixedKey][member] = struct{}{}
	}

	c.maybeSweepLocked(time.Now())
	return nil
}

func (c *LocalCache) Scard(key string) (int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	prefixedKey := withPrefix(key)

	if exp, ok := c.setExpirations[prefixedKey]; ok && time.Now().After(*exp) {
		return 0, nil
	}

	set, exists := c.setItems[prefixedKey]
	if !exists {
		return 0, nil
	}
	return int64(len(set)), nil
}
