// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"bytes"
	"slices"
	"sync"
)

// maxIndexCacheBytes bounds the indexes held in memory; the least recently
// used one leaves first.
const maxIndexCacheBytes = 256 << 20

// indexCache keeps whole indexes in memory by app and map hash, so the frames
// of one update are looked up without reading the store again.
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

func newIndexCache() *indexCache {
	return &indexCache{items: map[string]cachedIndex{}}
}

func (c *indexCache) get(hash string) (*Index, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.items[hash]
	if ok {
		c.touch(hash)
	}
	return item.index, ok
}

// put opens an index over data and keeps it, evicting the least recently
// used ones to make room. data must not exceed maxIndexCacheBytes.
func (c *indexCache) put(hash string, data []byte) (*Index, error) {
	index, err := OpenIndex(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if replaced, ok := c.items[hash]; ok {
		c.bytes -= replaced.size
	}
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
	if i := slices.Index(c.order, hash); i >= 0 {
		c.order = slices.Delete(c.order, i, i+1)
	}
	c.order = append(c.order, hash)
}
