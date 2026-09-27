// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"bytes"
	"sync"
)

// maxIndexCacheBytes bounds the indexes held in memory; the least recently
// used one leaves first.
const maxIndexCacheBytes = 256 << 20

// indexCache keeps whole indexes in memory by map hash, so the frames of one
// update are looked up without reading the store again.
type indexCache struct {
	mu    sync.Mutex
	items map[string]cachedIndex
	// order lists the hashes, least recently used first.
	order []string
	bytes int
}

type cachedIndex struct {
	index *Index
	size  int
}

// newIndexCache creates an empty cache of opened indexes.
func newIndexCache() *indexCache {
	return &indexCache{items: map[string]cachedIndex{}}
}

// get returns a cached index and marks it as most recently used; a cache
// miss returns nil, false.
func (c *indexCache) get(hash string) (*Index, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.items[hash]
	if ok {
		c.touch(hash)
	}
	return item.index, ok
}

// put opens an index over data, returning OpenIndex errors. Indexes larger
// than the cache budget are returned without being cached; otherwise older
// entries are evicted as needed. The returned index retains data.
func (c *indexCache) put(hash string, data []byte) (*Index, error) {
	index, err := OpenIndex(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if len(data) > maxIndexCacheBytes {
		return index, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[hash] = cachedIndex{index: index, size: len(data)}
	c.bytes += len(data)
	c.touch(hash)
	for c.bytes > maxIndexCacheBytes {
		oldest := c.order[0]
		c.order = c.order[1:]
		c.bytes -= c.items[oldest].size
		delete(c.items, oldest)
	}
	return index, nil
}

// touch moves a hash to the most recently used end.
func (c *indexCache) touch(hash string) {
	for i, other := range c.order {
		if other == hash {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, hash)
}
