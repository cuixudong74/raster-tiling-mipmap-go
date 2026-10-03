package mipmap

import (
	"container/list"
	"context"
	"sync"
)

// cacheItem is an entry in the LRU cache.
type cacheItem struct {
	key   tileKey
	tile  *Tile
	bytes int64
	elem  *list.Element
}

// pendingCall represents an in-flight tile generation. Concurrent callers
// for the same tile key wait on done and share the result.
type pendingCall struct {
	done chan struct{}
	tile *Tile
	err  error
}

// Cache is a bounded, thread-safe LRU cache of decoded tiles. It deduplicates
// concurrent generation of the same tile (singleflight) and never exposes a
// partially-written tile: tiles are only inserted once fully constructed.
type Cache struct {
	mu       sync.Mutex
	capBytes int64
	curBytes int64
	items    map[tileKey]*cacheItem
	order    *list.List // front = most recently used
	pending  map[tileKey]*pendingCall

	// observable metrics
	hits      int64
	misses    int64
	evictions int64
}

// cacheStats is a point-in-time snapshot of cache metrics.
type cacheStats struct {
	Hits      int64
	Misses    int64
	Evictions int64
	Bytes     int64
	Entries   int
	CapBytes  int64
}

// newCache creates a cache with the given byte capacity.
func newCache(capBytes int64) *Cache {
	return &Cache{
		capBytes: capBytes,
		items:    map[tileKey]*cacheItem{},
		order:    list.New(),
		pending:  map[tileKey]*pendingCall{},
	}
}

// getOrCreate returns the tile for key, generating it with gen if absent.
// Concurrent calls for the same key share one generation. If the caller's
// context is cancelled while waiting or generating, ErrCancelled is returned
// and no tile is inserted.
func (c *Cache) getOrCreate(ctx context.Context, key tileKey, gen func() (*Tile, error)) (*Tile, error) {
	c.mu.Lock()
	if item, ok := c.items[key]; ok {
		c.order.MoveToFront(item.elem)
		c.hits++
		c.mu.Unlock()
		return item.tile, nil
	}
	if p, ok := c.pending[key]; ok {
		c.misses++
		c.mu.Unlock()
		select {
		case <-p.done:
			if p.err != nil {
				return nil, p.err
			}
			return p.tile, nil
		case <-ctx.Done():
			return nil, ErrCancelled
		}
	}
	// Not cached and not pending: we are the generator.
	c.misses++
	p := &pendingCall{done: make(chan struct{})}
	c.pending[key] = p
	c.mu.Unlock()

	tile, err := gen()

	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, key)
	if err != nil {
		p.err = err
		close(p.done)
		return nil, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Generation was cancelled: do not publish.
		p.err = ErrCancelled
		close(p.done)
		return nil, ErrCancelled
	}
	if err := c.putLocked(key, tile); err != nil {
		p.err = err
		close(p.done)
		return nil, err
	}
	p.tile = tile
	close(p.done)
	return tile, nil
}

// putLocked inserts a tile, evicting LRU entries as needed. Caller must hold
// c.mu.
func (c *Cache) putLocked(key tileKey, tile *Tile) error {
	bytes := tile.Bytes()
	if bytes > c.capBytes {
		return ErrCacheFull
	}
	// Evict until there is room (or the cache is empty).
	for c.curBytes+bytes > c.capBytes && c.order.Len() > 0 {
		c.evictFrontLocked()
	}
	if c.curBytes+bytes > c.capBytes {
		return ErrCacheFull
	}
	// Replace if somehow present (should not happen via getOrCreate).
	if old, ok := c.items[key]; ok {
		c.order.Remove(old.elem)
		c.curBytes -= old.bytes
	}
	item := &cacheItem{key: key, tile: tile, bytes: bytes}
	item.elem = c.order.PushFront(item)
	c.items[key] = item
	c.curBytes += bytes
	return nil
}

// evictFrontLocked evicts the least-recently-used entry. Caller must hold c.mu.
func (c *Cache) evictFrontLocked() {
	elem := c.order.Back()
	if elem == nil {
		return
	}
	item := elem.Value.(*cacheItem)
	c.order.Remove(elem)
	delete(c.items, item.key)
	c.curBytes -= item.bytes
	c.evictions++
}

// evictLRU evicts least-recently-used entries until at least need bytes are
// free or the cache is empty. Caller must hold c.mu.
func (c *Cache) evictLRU(need int64) {
	for c.curBytes+need > c.capBytes && c.order.Len() > 0 {
		c.evictFrontLocked()
	}
}

// Stats returns a snapshot of cache metrics.
func (c *Cache) Stats() cacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cacheStats{
		Hits:      c.hits,
		Misses:    c.misses,
		Evictions: c.evictions,
		Bytes:     c.curBytes,
		Entries:   len(c.items),
		CapBytes:  c.capBytes,
	}
}
