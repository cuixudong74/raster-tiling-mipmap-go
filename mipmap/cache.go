package mipmap

import (
	"container/list"
	"sync"
)

// CacheMetrics 缓存运行指标，可通过 Metrics 快照读取。
type CacheMetrics struct {
	Hits      int64
	Misses    int64
	Evictions int64
	Bytes     int64 // 当前占用
}

// entry 是缓存中的一个元素。
type entry struct {
	tile *Tile
	size int64
}

// inflight 用于 singleflight：同一 Tile 的并发加载只执行一次。
type inflight struct {
	done chan struct{}
	tile *Tile
}

// TileCache 是有内存上界的 LRU Tile 缓存，并发安全。
//
// 语义：
//   - 容量以字节计，达到上界时按 LRU（最久未使用）淘汰。
//   - 单个 Tile 超过总容量时不缓存（Add 返回 false），避免缓存被撑爆。
//   - 缓存中的 Tile 不可变，读取方不得修改；因此不会读到半成品。
//   - GetOrLoad 对同一 TileID 的并发未命中只执行一次加载（singleflight）。
type TileCache struct {
	mu    sync.Mutex
	cap   int64
	used  int64
	ll    *list.List // 前端为最近使用
	items map[TileID]*list.Element
	loads map[TileID]*inflight

	metrics CacheMetrics
}

// NewTileCache 创建容量为 capBytes 的缓存。
func NewTileCache(capBytes int64) *TileCache {
	return &TileCache{
		cap:   capBytes,
		ll:    list.New(),
		items: make(map[TileID]*list.Element),
		loads: make(map[TileID]*inflight),
	}
}

// tileBytes 估算一块 Tile 的内存占用。
func tileBytes(t *Tile) int64 {
	return int64(len(t.Pix)) + 64 // 头部开销估算
}

// Get 查找 Tile，命中时返回并置为最近使用。
func (c *TileCache) Get(id TileID) (*Tile, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[id]; ok {
		c.ll.MoveToFront(el)
		c.metrics.Hits++
		return el.Value.(*entry).tile, true
	}
	c.metrics.Misses++
	return nil, false
}

// Add 放入 Tile，必要时按 LRU 淘汰直到满足容量。
// 单个 Tile 超过总容量时不缓存并返回 false。
func (c *TileCache) Add(t *Tile) bool {
	size := tileBytes(t)
	c.mu.Lock()
	defer c.mu.Unlock()
	if size > c.cap {
		return false
	}
	if el, ok := c.items[t.ID]; ok { // 已存在则替换并前移
		c.used -= el.Value.(*entry).size
		el.Value = &entry{tile: t, size: size}
		c.used += size
		c.ll.MoveToFront(el)
		return true
	}
	for c.used+size > c.cap && c.ll.Len() > 0 {
		c.evictLocked(c.ll.Back())
	}
	c.ll.PushFront(&entry{tile: t, size: size})
	c.items[t.ID] = c.ll.Front()
	c.used += size
	return true
}

// GetOrLoad 命中直接返回；未命中时调用 loader 加载并缓存。
// 同一 TileID 的并发加载只执行一次，其余调用等待结果，
// 不会读到半成品（loader 返回前 Tile 对等待者不可见）。
func (c *TileCache) GetOrLoad(id TileID, loader func() (*Tile, error)) (*Tile, error) {
	if t, ok := c.Get(id); ok {
		return t, nil
	}
	c.mu.Lock()
	if fl, ok := c.loads[id]; ok { // 已有加载在进行，等待
		c.mu.Unlock()
		<-fl.done
		return fl.tile, nil
	}
	fl := &inflight{done: make(chan struct{})}
	c.loads[id] = fl
	c.mu.Unlock()

	t, err := loader()

	c.mu.Lock()
	delete(c.loads, id)
	fl.tile = t
	close(fl.done)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	c.Add(t)
	return t, nil
}

// evictLocked 淘汰指定元素，调用方需持有锁。
func (c *TileCache) evictLocked(el *list.Element) {
	e := el.Value.(*entry)
	c.ll.Remove(el)
	delete(c.items, e.tile.ID)
	c.used -= e.size
	c.metrics.Evictions++
}

// Metrics 返回当前指标快照。
func (c *TileCache) Metrics() CacheMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.metrics
	m.Bytes = c.used
	return m
}
