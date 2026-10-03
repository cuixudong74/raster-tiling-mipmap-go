package mipmap

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math/bits"
	"sync"
)

// tileCoord is a tile coordinate pair.
type tileCoord struct {
	tx, ty int
}

// Mipmap holds a source image, its mipmap chain configuration and a bounded
// tile cache. Tiles are generated lazily on demand (GetTile) or eagerly in
// bulk (Generate). All generation is deterministic: the same source and
// configuration produce pixel-identical levels regardless of tile division,
// parallelism or generation order.
type Mipmap struct {
	src      *Source
	cfg      Config
	cache    *Cache
	maxLevel int
	levelW   []int // levelW[L] = width of level L
	levelH   []int
	tilesX   []int // tilesX[L] = number of tile columns at level L
	tilesY   []int
}

// NewMipmap builds a Mipmap from a source image and configuration. The source
// is immutable; the mipmap copies pixels into tiles as needed.
func NewMipmap(src *Source, cfg Config) (*Mipmap, error) {
	if src == nil {
		return nil, ErrInvalidImage
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	maxDim := src.W
	if src.H > maxDim {
		maxDim = src.H
	}
	maxLevel := ceilLog2(maxDim)
	m := &Mipmap{
		src:      src,
		cfg:      cfg,
		cache:    newCache(cfg.MaxCacheBytes),
		maxLevel: maxLevel,
		levelW:   make([]int, maxLevel+1),
		levelH:   make([]int, maxLevel+1),
		tilesX:   make([]int, maxLevel+1),
		tilesY:   make([]int, maxLevel+1),
	}
	w, h := src.W, src.H
	for L := 0; L <= maxLevel; L++ {
		m.levelW[L] = w
		m.levelH[L] = h
		m.tilesX[L] = ceilDiv(w, cfg.TileSize)
		m.tilesY[L] = ceilDiv(h, cfg.TileSize)
		w = ceilDiv(w, 2)
		h = ceilDiv(h, 2)
	}
	return m, nil
}

// MaxLevel returns the highest mip level (0-based). Level 0 is the source.
func (m *Mipmap) MaxLevel() int { return m.maxLevel }

// LevelDims returns the pixel dimensions of level L.
func (m *Mipmap) LevelDims(level int) (int, int) {
	if level < 0 || level > m.maxLevel {
		return 0, 0
	}
	return m.levelW[level], m.levelH[level]
}

// TileGrid returns the number of tile columns and rows at level L.
func (m *Mipmap) TileGrid(level int) (int, int) {
	if level < 0 || level > m.maxLevel {
		return 0, 0
	}
	return m.tilesX[level], m.tilesY[level]
}

// levelDims returns the pixel dimensions of level L without validation.
func (m *Mipmap) levelDims(level int) (int, int) {
	return m.levelW[level], m.levelH[level]
}

// tileGrid returns the tile grid dimensions of level L without validation.
func (m *Mipmap) tileGrid(level int) (int, int) {
	return m.tilesX[level], m.tilesY[level]
}

// GetTile returns the tile at (tx,ty) on level L, generating it (and any
// required parent tiles) if necessary. Generation is deterministic and safe
// for concurrent use. If ctx is cancelled, ErrCancelled is returned and no
// partial output is cached.
func (m *Mipmap) GetTile(ctx context.Context, level, tx, ty int) (*Tile, error) {
	if level < 0 || level > m.maxLevel {
		return nil, fmt.Errorf("%w: level %d not in [0,%d]", ErrInvalidLevel, level, m.maxLevel)
	}
	tilesX, tilesY := m.tileGrid(level)
	if tx < 0 || tx >= tilesX || ty < 0 || ty >= tilesY {
		return nil, fmt.Errorf("%w: tile (%d,%d) not in grid %dx%d at level %d", ErrInvalidTile, tx, ty, tilesX, tilesY, level)
	}
	key := tileKey{level: level, tx: tx, ty: ty}
	return m.cache.getOrCreate(ctx, key, func() (*Tile, error) {
		return m.generateTile(ctx, level, tx, ty)
	})
}

// Generate eagerly generates every tile of every level, level by level, with
// bounded concurrency. It returns the first error encountered (including
// context cancellation). On cancellation, no partial output is cached and
// subsequent requests are unaffected.
func (m *Mipmap) Generate(ctx context.Context) error {
	for level := 0; level <= m.maxLevel; level++ {
		if err := ctx.Err(); err != nil {
			return ErrCancelled
		}
		if err := m.generateLevel(ctx, level); err != nil {
			return err
		}
	}
	return nil
}

// generateLevel generates all tiles of a single level concurrently with a
// bounded worker pool.
func (m *Mipmap) generateLevel(ctx context.Context, level int) error {
	tilesX, tilesY := m.tileGrid(level)
	total := tilesX * tilesY
	if total == 0 {
		return nil
	}
	workers := m.cfg.workerCount()
	if workers > total {
		workers = total
	}
	coords := make(chan tileCoord, total)
	for ty := 0; ty < tilesY; ty++ {
		for tx := 0; tx < tilesX; tx++ {
			coords <- tileCoord{tx: tx, ty: ty}
		}
	}
	close(coords)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range coords {
				if err := ctx.Err(); err != nil {
					return
				}
				if _, err := m.GetTile(ctx, level, c.tx, c.ty); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if ctx.Err() != nil {
		return ErrCancelled
	}
	return nil
}

// generateTile constructs a single tile. Level-0 tiles are cropped from the
// source; deeper tiles are downsampled from the previous level.
func (m *Mipmap) generateTile(ctx context.Context, level, tx, ty int) (*Tile, error) {
	if err := ctx.Err(); err != nil {
		return nil, ErrCancelled
	}
	if level == 0 {
		return m.cropLevel0Tile(tx, ty), nil
	}
	return m.downsampleTile(ctx, level, tx, ty)
}

// cropLevel0Tile copies the source pixels covered by tile (tx,ty) at level 0.
func (m *Mipmap) cropLevel0Tile(tx, ty int) *Tile {
	ts := m.cfg.TileSize
	w, h := tileDims(m.src.W, m.src.H, ts, tx, ty)
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	x0, y0 := tx*ts, ty*ts
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			si := (y0+y)*m.src.Img.Stride + (x0+x)*4
			di := y*dst.Stride + x*4
			dst.Pix[di+0] = m.src.Img.Pix[si+0]
			dst.Pix[di+1] = m.src.Img.Pix[si+1]
			dst.Pix[di+2] = m.src.Img.Pix[si+2]
			dst.Pix[di+3] = m.src.Img.Pix[si+3]
		}
	}
	return &Tile{Level: 0, TX: tx, TY: ty, Img: dst}
}

// downsampleTile constructs a level-L tile by downsampling level L-1. Each
// output pixel is a 2x2 box average of the corresponding parent pixels, with
// edge clamping. The four parent tiles covering the tile's footprint are
// fetched once and reused.
func (m *Mipmap) downsampleTile(ctx context.Context, level, tx, ty int) (*Tile, error) {
	ts := m.cfg.TileSize
	pLevel := level - 1
	pW, pH := m.levelDims(pLevel)
	pTilesX, pTilesY := m.tileGrid(pLevel)
	w, h := tileDims(m.levelW[level], m.levelH[level], ts, tx, ty)
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))

	// Fetch the up-to-4 parent tiles covering this tile's footprint.
	parents := map[image.Point]*Tile{}
	for _, d := range [4]image.Point{
		{2 * tx, 2 * ty},
		{2*tx + 1, 2 * ty},
		{2 * tx, 2*ty + 1},
		{2*tx + 1, 2*ty + 1},
	} {
		kx := clampInt(d.X, 0, pTilesX-1)
		ky := clampInt(d.Y, 0, pTilesY-1)
		p := image.Point{X: kx, Y: ky}
		if _, ok := parents[p]; ok {
			continue
		}
		t, err := m.GetTile(ctx, pLevel, kx, ky)
		if err != nil {
			return nil, err
		}
		parents[p] = t
	}

	for ly := 0; ly < h; ly++ {
		for lx := 0; lx < w; lx++ {
			x := tx*ts + lx
			y := ty*ts + ly
			cx := [4]int{2 * x, 2*x + 1, 2 * x, 2*x + 1}
			cy := [4]int{2 * y, 2 * y, 2*y + 1, 2*y + 1}
			var px [4]color.NRGBA
			for k := 0; k < 4; k++ {
				qx := clampInt(cx[k], 0, pW-1)
				qy := clampInt(cy[k], 0, pH-1)
				ptx, pty := qx/ts, qy/ts
				t := parents[image.Point{X: ptx, Y: pty}]
				plx, ply := qx-ptx*ts, qy-pty*ts
				i := ply*t.Img.Stride + plx*4
				px[k] = color.NRGBA{
					R: t.Img.Pix[i+0],
					G: t.Img.Pix[i+1],
					B: t.Img.Pix[i+2],
					A: t.Img.Pix[i+3],
				}
			}
			dst.SetNRGBA(lx, ly, downsamplePixel(px))
		}
	}
	return &Tile{Level: level, TX: tx, TY: ty, Img: dst}, nil
}

// AssembleLevel reconstructs a full level image by tiling its tiles. This is
// used for verification and as the reference path for sampling.
func (m *Mipmap) AssembleLevel(ctx context.Context, level int) (*image.NRGBA, error) {
	if level < 0 || level > m.maxLevel {
		return nil, fmt.Errorf("%w: level %d not in [0,%d]", ErrInvalidLevel, level, m.maxLevel)
	}
	W, H := m.levelDims(level)
	ts := m.cfg.TileSize
	tilesX, tilesY := m.tileGrid(level)
	out := image.NewNRGBA(image.Rect(0, 0, W, H))
	for ty := 0; ty < tilesY; ty++ {
		for tx := 0; tx < tilesX; tx++ {
			t, err := m.GetTile(ctx, level, tx, ty)
			if err != nil {
				return nil, err
			}
			x0, y0 := tx*ts, ty*ts
			for y := 0; y < t.H(); y++ {
				for x := 0; x < t.W(); x++ {
					si := y*t.Img.Stride + x*4
					di := (y0+y)*out.Stride + (x0+x)*4
					out.Pix[di+0] = t.Img.Pix[si+0]
					out.Pix[di+1] = t.Img.Pix[si+1]
					out.Pix[di+2] = t.Img.Pix[si+2]
					out.Pix[di+3] = t.Img.Pix[si+3]
				}
			}
		}
	}
	return out, nil
}

// CacheStats is a point-in-time snapshot of the tile cache metrics.
type CacheStats struct {
	Hits      int64
	Misses    int64
	Evictions int64
	Bytes     int64
	CapBytes  int64
	Entries   int
}

// Stats returns a snapshot of the tile cache metrics. Hits, misses and
// evictions are observable so callers can verify cache behaviour.
func (m *Mipmap) Stats() CacheStats {
	s := m.cache.Stats()
	return CacheStats{
		Hits:      s.Hits,
		Misses:    s.Misses,
		Evictions: s.Evictions,
		Bytes:     s.Bytes,
		CapBytes:  s.CapBytes,
		Entries:   s.Entries,
	}
}

// ceilDiv returns ceil(a/b) for non-negative a, b.
func ceilDiv(a, b int) int {
	return (a + b - 1) / b
}

// ceilLog2 returns the smallest n such that 2^n >= x. For x<=1 returns 0.
func ceilLog2(x int) int {
	if x <= 1 {
		return 0
	}
	return bits.Len(uint(x - 1))
}
