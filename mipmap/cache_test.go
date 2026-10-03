package mipmap

import (
	"fmt"
	"image"
	"sync"
	"testing"
)

// mkTile 构造指定层级与像素大小的测试 Tile。
func mkTile(level, tx, ty, w, h int) *Tile {
	return &Tile{
		ID:   TileID{Level: level, TX: tx, TY: ty},
		Rect: image.Rect(0, 0, w, h),
		Pix:  make([]uint8, w*h*4),
	}
}

// TestCacheHitMiss 命中与未命中可观测。
func TestCacheHitMiss(t *testing.T) {
	c := NewTileCache(1 << 20)
	tile := mkTile(0, 0, 0, 8, 8)
	if !c.Add(tile) {
		t.Fatal("add failed")
	}
	if _, ok := c.Get(tile.ID); !ok {
		t.Fatal("expected hit")
	}
	c.Get(TileID{Level: 9, TX: 9, TY: 9})
	m := c.Metrics()
	if m.Hits != 1 || m.Misses != 1 {
		t.Fatalf("metrics = %+v", m)
	}
}

// TestCacheEviction 容量达到上界时按 LRU 淘汰，占用不越界。
func TestCacheEviction(t *testing.T) {
	tl := mkTile(0, 0, 0, 16, 16)
	sz := tileBytes(tl)
	cap := sz*4 + 64 // 容纳 4 块
	c := NewTileCache(cap)
	var ids []TileID
	for i := 0; i < 4; i++ {
		ti := mkTile(0, i, 0, 16, 16)
		ids = append(ids, ti.ID)
		if !c.Add(ti) {
			t.Fatalf("add %d failed", i)
		}
	}
	// 访问第 0 块使其成为最近使用。
	if _, ok := c.Get(ids[0]); !ok {
		t.Fatal("expected hit for tile 0")
	}
	// 加入第 5 块，应淘汰最久未使用的第 1 块。
	t5 := mkTile(0, 4, 0, 16, 16)
	if !c.Add(t5) {
		t.Fatal("add 5th failed")
	}
	if _, ok := c.Get(ids[1]); ok {
		t.Fatal("tile 1 should have been evicted (LRU)")
	}
	if _, ok := c.Get(ids[0]); !ok {
		t.Fatal("tile 0 should survive (recently used)")
	}
	m := c.Metrics()
	if m.Evictions != 1 {
		t.Fatalf("evictions = %d want 1", m.Evictions)
	}
	if m.Bytes > cap {
		t.Fatalf("bytes %d exceed capacity %d", m.Bytes, cap)
	}
}

// TestCacheCapacityBound 大量请求下缓存占用有界。
func TestCacheCapacityBound(t *testing.T) {
	cap := int64(1 << 16)
	c := NewTileCache(cap)
	for i := 0; i < 1000; i++ {
		c.Add(mkTile(0, i, 0, 16, 16))
	}
	m := c.Metrics()
	if m.Bytes > cap {
		t.Fatalf("bytes %d exceed capacity %d", m.Bytes, cap)
	}
	if m.Evictions == 0 {
		t.Fatal("expected evictions under pressure")
	}
}

// TestCacheOversizedTile 单块超过容量时不缓存。
func TestCacheOversizedTile(t *testing.T) {
	c := NewTileCache(128)
	big := mkTile(0, 0, 0, 64, 64) // 16KB，远超容量
	if c.Add(big) {
		t.Fatal("oversized tile should not be cached")
	}
	if _, ok := c.Get(big.ID); ok {
		t.Fatal("oversized tile must not be retrievable")
	}
}

// TestCacheGetOrLoadSingleflight 并发未命中只加载一次。
func TestCacheGetOrLoadSingleflight(t *testing.T) {
	c := NewTileCache(1 << 20)
	id := TileID{Level: 0, TX: 0, TY: 0}
	var mu sync.Mutex
	loads := 0
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tile, err := c.GetOrLoad(id, func() (*Tile, error) {
				mu.Lock()
				loads++
				mu.Unlock()
				return mkTile(0, 0, 0, 8, 8), nil
			})
			if err != nil || tile == nil {
				t.Error("GetOrLoad failed")
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if loads != 1 {
		t.Fatalf("loads = %d want 1 (singleflight)", loads)
	}
}

// TestCacheGetOrLoadError 加载失败不缓存、不阻塞后续加载。
func TestCacheGetOrLoadError(t *testing.T) {
	c := NewTileCache(1 << 20)
	id := TileID{Level: 1, TX: 2, TY: 3}
	if _, err := c.GetOrLoad(id, func() (*Tile, error) {
		return nil, fmt.Errorf("decode failed")
	}); err == nil {
		t.Fatal("expected loader error")
	}
	// 失败后再次加载应能成功。
	tile, err := c.GetOrLoad(id, func() (*Tile, error) {
		return mkTile(1, 2, 3, 8, 8), nil
	})
	if err != nil || tile == nil {
		t.Fatal("load after error failed")
	}
}

// TestCacheConcurrent 并发读写无数据竞争（配合 -race），占用始终有界。
func TestCacheConcurrent(t *testing.T) {
	cap := int64(1 << 16)
	c := NewTileCache(cap)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				id := TileID{Level: 0, TX: i % 50, TY: g}
				if _, ok := c.Get(id); !ok {
					c.Add(mkTile(0, i%50, g, 16, 16))
				}
				if m := c.Metrics(); m.Bytes > cap {
					t.Errorf("bytes %d exceed capacity %d", m.Bytes, cap)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
