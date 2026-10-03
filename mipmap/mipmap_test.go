package mipmap

import (
	"context"
	"image"
	"image/color"
	"math"
	"sync"
	"testing"
	"time"
)

// --- helpers ---

func nrgba(r, g, b, a uint8) color.NRGBA {
	return color.NRGBA{R: r, G: g, B: b, A: a}
}

// assembleLevelRef reconstructs a level from tiles via the public API.
func assembleLevelRef(t *testing.T, m *Mipmap, level int) *image.NRGBA {
	t.Helper()
	img, err := m.AssembleLevel(context.Background(), level)
	if err != nil {
		t.Fatalf("assemble level %d: %v", level, err)
	}
	return img
}

// imagesEqual compares two NRGBA images pixel by pixel.
func imagesEqual(a, b *image.NRGBA) bool {
	if a.Rect != b.Rect {
		return false
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			return false
		}
	}
	return true
}

// --- level dimensions & tile grid ---

func TestLevelDimsNonPowerOfTwo(t *testing.T) {
	cases := []struct {
		w, h     int
		maxLevel int
	}{
		{5, 3, 3},   // 5x3 -> 3x2 -> 2x1 -> 1x1
		{7, 5, 3},   // 7x5 -> 4x3 -> 2x2 -> 1x1
		{3, 3, 2},   // 3x3 -> 2x2 -> 1x1
		{1, 1, 0},   // single pixel
		{2, 1, 1},   // 2x1 -> 1x1
		{1, 2, 1},   // 1x2 -> 1x1
		{16, 16, 4}, // power of two
	}
	for _, c := range cases {
		src, err := NewSolidSource(c.w, c.h, nrgba(255, 0, 0, 255))
		if err != nil {
			t.Fatal(err)
		}
		m, err := NewMipmap(src, DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		if m.MaxLevel() != c.maxLevel {
			t.Errorf("src %dx%d: maxLevel = %d, want %d", c.w, c.h, m.MaxLevel(), c.maxLevel)
		}
		// Verify level dims follow ceil division.
		w, h := c.w, c.h
		for L := 0; L <= m.MaxLevel(); L++ {
			lw, lh := m.LevelDims(L)
			if lw != w || lh != h {
				t.Errorf("src %dx%d level %d: dims = %dx%d, want %dx%d", c.w, c.h, L, lw, lh, w, h)
			}
			w = ceilDiv(w, 2)
			h = ceilDiv(h, 2)
		}
	}
}

func TestTileGridCoversLevel(t *testing.T) {
	src, _ := NewSolidSource(5, 3, nrgba(0, 0, 0, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 2
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for L := 0; L <= m.MaxLevel(); L++ {
		lw, lh := m.LevelDims(L)
		tilesX, tilesY := m.TileGrid(L)
		// The union of tiles must exactly cover the level.
		for ty := 0; ty < tilesY; ty++ {
			for tx := 0; tx < tilesX; tx++ {
				w, h := tileDims(lw, lh, cfg.TileSize, tx, ty)
				if w <= 0 || h <= 0 {
					t.Errorf("level %d tile (%d,%d): degenerate size %dx%d", L, tx, ty, w, h)
				}
				x0, y0 := tx*cfg.TileSize, ty*cfg.TileSize
				if x0+w > lw || y0+h > lh {
					t.Errorf("level %d tile (%d,%d): out of bounds", L, tx, ty)
				}
			}
		}
		// Last tile column/row must reach the level edge.
		lastW, lastH := tileDims(lw, lh, cfg.TileSize, tilesX-1, tilesY-1)
		if (tilesX-1)*cfg.TileSize+lastW != lw {
			t.Errorf("level %d: tile columns do not cover width %d", L, lw)
		}
		if (tilesY-1)*cfg.TileSize+lastH != lh {
			t.Errorf("level %d: tile rows do not cover height %d", L, lh)
		}
	}
}

// --- completeness: assembled level reconstructs the source ---

func TestAssembleLevel0EqualsSource(t *testing.T) {
	src, _ := NewCheckerSource(13, 11, 3, nrgba(255, 0, 0, 255), nrgba(0, 0, 255, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 4
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	assembled := assembleLevelRef(t, m, 0)
	if !imagesEqual(src.Img, assembled) {
		t.Error("assembled level 0 does not match source")
	}
}

func TestAssembleAllLevelsComplete(t *testing.T) {
	src, _ := NewGradientSource(17, 13, nrgba(255, 0, 0, 255), nrgba(0, 255, 0, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 4
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for L := 0; L <= m.MaxLevel(); L++ {
		img := assembleLevelRef(t, m, L)
		w, h := m.LevelDims(L)
		if img.Rect.Dx() != w || img.Rect.Dy() != h {
			t.Errorf("level %d: assembled size %dx%d, want %dx%d", L, img.Rect.Dx(), img.Rect.Dy(), w, h)
		}
		// The pixel buffer must be exactly w*h*4 bytes (no gaps, no overflow).
		if len(img.Pix) != w*h*4 {
			t.Errorf("level %d: pix length %d, want %d", L, len(img.Pix), w*h*4)
		}
	}
}

// --- determinism: same source -> identical levels regardless of tiling/order ---

func TestDeterminismAcrossTileSizes(t *testing.T) {
	src, _ := NewCheckerSource(33, 29, 5, nrgba(255, 0, 0, 255), nrgba(0, 0, 255, 255))
	refCfg := DefaultConfig()
	refCfg.TileSize = 8
	ref, err := NewMipmap(src, refCfg)
	if err != nil {
		t.Fatal(err)
	}
	refLevels := make([]*image.NRGBA, ref.MaxLevel()+1)
	for L := 0; L <= ref.MaxLevel(); L++ {
		refLevels[L] = assembleLevelRef(t, ref, L)
	}

	for _, ts := range []int{2, 4, 16, 32} {
		cfg := DefaultConfig()
		cfg.TileSize = ts
		m, err := NewMipmap(src, cfg)
		if err != nil {
			t.Fatal(err)
		}
		for L := 0; L <= m.MaxLevel(); L++ {
			img := assembleLevelRef(t, m, L)
			if !imagesEqual(refLevels[L], img) {
				t.Errorf("tile size %d level %d: assembled level differs from reference (tile size 8)", ts, L)
			}
		}
	}
}

func TestDeterminismAcrossGenerationOrder(t *testing.T) {
	src, _ := NewCheckerSource(20, 20, 3, nrgba(255, 0, 0, 255), nrgba(0, 255, 0, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 4
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Generate all tiles eagerly (concurrent, out of order).
	if err := m.Generate(context.Background()); err != nil {
		t.Fatal(err)
	}
	eagerLevels := make([]*image.NRGBA, m.MaxLevel()+1)
	for L := 0; L <= m.MaxLevel(); L++ {
		eagerLevels[L] = assembleLevelRef(t, m, L)
	}

	// Build a fresh mipmap and generate lazily in a different order.
	m2, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for L := m2.MaxLevel(); L >= 0; L-- {
		tilesX, tilesY := m2.TileGrid(L)
		for ty := tilesY - 1; ty >= 0; ty-- {
			for tx := tilesX - 1; tx >= 0; tx-- {
				if _, err := m2.GetTile(context.Background(), L, tx, ty); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for L := 0; L <= m2.MaxLevel(); L++ {
		img := assembleLevelRef(t, m2, L)
		if !imagesEqual(eagerLevels[L], img) {
			t.Errorf("level %d: lazy reverse-order generation differs from eager", L)
		}
	}
}

// --- downsample correctness vs reference ---

func referenceDownsamplePixel(src *image.NRGBA, x, y int) color.NRGBA {
	srcW := src.Rect.Dx()
	srcH := src.Rect.Dy()
	clamp := func(v, lo, hi int) int {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	cx := [4]int{2 * x, 2*x + 1, 2 * x, 2*x + 1}
	cy := [4]int{2 * y, 2 * y, 2*y + 1, 2*y + 1}
	var r, g, b, a float64
	for k := 0; k < 4; k++ {
		qx := clamp(cx[k], 0, srcW-1)
		qy := clamp(cy[k], 0, srcH-1)
		i := qy*src.Stride + qx*4
		af := float64(src.Pix[i+3]) / 255
		r += float64(src.Pix[i+0]) * af
		g += float64(src.Pix[i+1]) * af
		b += float64(src.Pix[i+2]) * af
		a += af
	}
	r /= 4
	g /= 4
	b /= 4
	a /= 4
	if a <= 0 {
		return color.NRGBA{0, 0, 0, 0}
	}
	return color.NRGBA{
		R: clampRound(r / a),
		G: clampRound(g / a),
		B: clampRound(b / a),
		A: clampRound(a * 255),
	}
}

func TestDownsampleMatchesReference(t *testing.T) {
	src, _ := NewCheckerSource(13, 11, 3, nrgba(255, 0, 0, 200), nrgba(0, 0, 255, 100))
	cfg := DefaultConfig()
	cfg.TileSize = 4
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	prev := src.Img
	for L := 1; L <= m.MaxLevel(); L++ {
		got := assembleLevelRef(t, m, L)
		w, h := m.LevelDims(L)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				want := referenceDownsamplePixel(prev, x, y)
				i := y*got.Stride + x*4
				gotPx := color.NRGBA{got.Pix[i], got.Pix[i+1], got.Pix[i+2], got.Pix[i+3]}
				if gotPx != want {
					t.Errorf("level %d (%d,%d): got %v, want %v", L, x, y, gotPx, want)
				}
			}
		}
		prev = got
	}
}

// --- alpha channel handling ---

func TestAlphaDownsample(t *testing.T) {
	// A column of fully transparent pixels next to opaque ones.
	src, _ := NewSource(4, 1, func(x, y int) color.NRGBA {
		if x < 2 {
			return nrgba(255, 0, 0, 255)
		}
		return nrgba(0, 0, 0, 0)
	})
	cfg := DefaultConfig()
	cfg.TileSize = 2
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Level 1 is 2x1. Pixel 0 = average of (255,0,0,255)x2 -> (255,0,0,255).
	// Pixel 1 = average of transparent x2 -> (0,0,0,0).
	level1 := assembleLevelRef(t, m, 1)
	p0 := color.NRGBA{level1.Pix[0], level1.Pix[1], level1.Pix[2], level1.Pix[3]}
	p1 := color.NRGBA{level1.Pix[4], level1.Pix[5], level1.Pix[6], level1.Pix[7]}
	if p0 != (nrgba(255, 0, 0, 255)) {
		t.Errorf("level1 pixel0 = %v, want opaque red", p0)
	}
	if p1 != (nrgba(0, 0, 0, 0)) {
		t.Errorf("level1 pixel1 = %v, want transparent", p1)
	}
}

func TestAlphaPremultipliedNoFringe(t *testing.T) {
	// Opaque red next to transparent: premultiplied downsample must not
	// produce a dark fringe at the boundary.
	src, _ := NewSource(2, 2, func(x, y int) color.NRGBA {
		if x == 0 {
			return nrgba(255, 0, 0, 255)
		}
		return nrgba(0, 0, 0, 0)
	})
	cfg := DefaultConfig()
	cfg.TileSize = 2
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	level1 := assembleLevelRef(t, m, 1)
	// Average of 2 opaque red + 2 transparent: premult = (127.5,0,0,0.5)
	// -> unpremult = (255,0,0,127). No dark fringe.
	p := color.NRGBA{level1.Pix[0], level1.Pix[1], level1.Pix[2], level1.Pix[3]}
	if p.R != 255 || (p.A != 127 && p.A != 128) {
		t.Errorf("alpha fringe: got %v, want R=255 and A in {127,128}", p)
	}
}

// --- single pixel ---

func TestSinglePixel(t *testing.T) {
	src, _ := NewSolidSource(1, 1, nrgba(123, 200, 50, 200))
	cfg := DefaultConfig()
	cfg.TileSize = 4
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if m.MaxLevel() != 0 {
		t.Errorf("single pixel: maxLevel = %d, want 0", m.MaxLevel())
	}
	img := assembleLevelRef(t, m, 0)
	p := color.NRGBA{img.Pix[0], img.Pix[1], img.Pix[2], img.Pix[3]}
	if p != (nrgba(123, 200, 50, 200)) {
		t.Errorf("single pixel = %v, want {123,200,50,200}", p)
	}
	// Sampling a single pixel should return the pixel color.
	s := NewSampler(m)
	c, err := s.Sample(context.Background(), SampleRequest{U: 0.5, V: 0.5, DUDX: 0.01, DVDY: 0.01})
	if err != nil {
		t.Fatal(err)
	}
	if c != (nrgba(123, 200, 50, 200)) {
		t.Errorf("single pixel sample = %v, want {123,200,50,200}", c)
	}
}

// --- seamless sampling across tile boundaries ---

// imageFetcher is a reference levelPixelFetcher that reads from fully
// assembled level images.
type imageFetcher map[int]*image.NRGBA

func (f imageFetcher) fetch(_ context.Context, level, x, y int) (Pixel, error) {
	img := f[level]
	W, H := img.Rect.Dx(), img.Rect.Dy()
	x = clampInt(x, 0, W-1)
	y = clampInt(y, 0, H-1)
	i := y*img.Stride + x*4
	r := float64(img.Pix[i+0]) / 255
	g := float64(img.Pix[i+1]) / 255
	b := float64(img.Pix[i+2]) / 255
	a := float64(img.Pix[i+3]) / 255
	return Pixel{R: r * a, G: g * a, B: b * a, A: a}, nil
}

func TestSeamlessSamplingAcrossTiles(t *testing.T) {
	src, _ := NewCheckerSource(37, 31, 5, nrgba(255, 0, 0, 255), nrgba(0, 255, 0, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 8
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Assemble all levels for the reference fetcher.
	refLevels := make(imageFetcher)
	for L := 0; L <= m.MaxLevel(); L++ {
		refLevels[L] = assembleLevelRef(t, m, L)
	}

	s := NewSampler(m)
	ctx := context.Background()
	// Sample a dense grid of points, including points that land exactly on
	// tile boundaries.
	for gy := 0; gy <= 64; gy++ {
		for gx := 0; gx <= 64; gx++ {
			u := float64(gx) / 64.0
			v := float64(gy) / 64.0
			// Use a footprint that spans multiple texels to exercise
			// trilinear and anisotropic paths.
			d := 0.05
			req := SampleRequest{
				U: u, V: v,
				DUDX: d, DVDY: d,
			}
			got, err := s.Sample(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			// Reference: the exact same sampling math, but reading from
			// assembled whole-level images instead of tiles.
			ref, err := samplePixel(ctx, refLevels.fetch, req, src.W, src.H, m.MaxLevel(), m.cfg)
			if err != nil {
				t.Fatal(err)
			}
			want := pixelToNRGBA(ref)
			if got != want {
				t.Errorf("sample (%.3f,%.3f): got %v, want %v", u, v, got, want)
			}
		}
	}
}

func TestSeamlessBilinearAtBoundary(t *testing.T) {
	// A smooth gradient: bilinear interpolation across a tile boundary must
	// equal bilinear interpolation on the whole image.
	src, _ := NewGradientSource(16, 16, nrgba(0, 0, 0, 255), nrgba(255, 255, 255, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 8
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	refLevels := make(imageFetcher)
	refLevels[0] = assembleLevelRef(t, m, 0)
	s := NewSampler(m)
	ctx := context.Background()
	// Sample right at the tile boundary (x=8 -> u=0.5) with magnification
	// (small footprint) so we get pure bilinear.
	for _, u := range []float64{0.49, 0.5, 0.51} {
		req := SampleRequest{U: u, V: 0.5, DUDX: 0.001, DVDY: 0.001}
		got, err := s.Sample(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		ref, err := samplePixel(ctx, refLevels.fetch, req, 16, 16, m.MaxLevel(), m.cfg)
		if err != nil {
			t.Fatal(err)
		}
		want := pixelToNRGBA(ref)
		if got != want {
			t.Errorf("u=%.2f: got %v, want %v", u, got, want)
		}
	}
}

// --- mip sampling & LOD ---

func TestMipSamplingSmoothTransition(t *testing.T) {
	src, _ := NewCheckerSource(64, 64, 8, nrgba(255, 0, 0, 255), nrgba(0, 0, 255, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 16
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSampler(m)
	ctx := context.Background()
	// As the footprint grows, the sampled color should transition smoothly
	// from the checkerboard toward the average (purple-ish).
	var prev color.NRGBA
	for i := 0; i <= 10; i++ {
		d := 0.01 * math.Pow(2, float64(i))
		c, err := s.Sample(ctx, SampleRequest{U: 0.5, V: 0.5, DUDX: d, DVDY: d})
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			// Channel values should change monotonically (smooth transition).
			_ = prev
		}
		prev = c
	}
	// At a very large footprint, the result should be close to the average
	// of red and blue = (127,0,127).
	c, err := s.Sample(ctx, SampleRequest{U: 0.5, V: 0.5, DUDX: 10, DVDY: 10})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(c.R)-127) > 2 || math.Abs(float64(c.B)-127) > 2 {
		t.Errorf("large footprint: got %v, want ~{127,0,127,255}", c)
	}
}

func TestAnisotropicSampleCountDeterministic(t *testing.T) {
	src, _ := NewSolidSource(64, 64, nrgba(100, 150, 200, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 16
	cfg.MaxAnisotropy = 8
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSampler(m)
	ctx := context.Background()
	// Anisotropic footprint: 8x ratio. Sample twice and verify identical.
	req := SampleRequest{U: 0.5, V: 0.5, DUDX: 0.08, DVDX: 0.01, DVDY: 0.01, DUDY: 0.01}
	c1, err := s.Sample(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := s.Sample(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 {
		t.Errorf("anisotropic samples differ: %v vs %v", c1, c2)
	}
}

// --- cache eviction & bounded memory ---

func TestCacheEviction(t *testing.T) {
	src, _ := NewCheckerSource(64, 64, 4, nrgba(255, 0, 0, 255), nrgba(0, 255, 0, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 16
	// One tile = 16*16*4 = 1024 bytes. Cache holds ~4 tiles.
	cfg.MaxCacheBytes = 4 * 1024
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Generate all tiles at level 0 (16 tiles), forcing eviction.
	for ty := 0; ty < 4; ty++ {
		for tx := 0; tx < 4; tx++ {
			if _, err := m.GetTile(context.Background(), 0, tx, ty); err != nil {
				t.Fatal(err)
			}
		}
	}
	stats := m.Stats()
	if stats.Bytes > cfg.MaxCacheBytes {
		t.Errorf("cache bytes %d exceed cap %d", stats.Bytes, cfg.MaxCacheBytes)
	}
	if stats.Evictions == 0 {
		t.Error("expected evictions with small cache, got 0")
	}
	if stats.Entries > 4 {
		t.Errorf("cache entries %d exceed expected ~4", stats.Entries)
	}
	// Re-fetching an evicted tile should be a miss and regenerate.
	before := m.Stats().Misses
	if _, err := m.GetTile(context.Background(), 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	after := m.Stats().Misses
	if after <= before {
		t.Error("expected a cache miss on re-fetch, got none")
	}
}

func TestCacheHitsObservable(t *testing.T) {
	src, _ := NewSolidSource(32, 32, nrgba(255, 0, 0, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 16
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// First fetch = miss.
	if _, err := m.GetTile(context.Background(), 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	// Second fetch = hit.
	if _, err := m.GetTile(context.Background(), 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	stats := m.Stats()
	if stats.Hits < 1 {
		t.Errorf("expected >= 1 hit, got %d", stats.Hits)
	}
	if stats.Misses < 1 {
		t.Errorf("expected >= 1 miss, got %d", stats.Misses)
	}
}

// --- concurrent generation & cancellation ---

func TestConcurrentGeneration(t *testing.T) {
	src, _ := NewCheckerSource(64, 64, 4, nrgba(255, 0, 0, 255), nrgba(0, 255, 0, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 8
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Generate(context.Background()); err != nil {
		t.Fatal(err)
	}
	// All tiles should be cached.
	stats := m.Stats()
	expected := 0
	for L := 0; L <= m.MaxLevel(); L++ {
		tx, ty := m.TileGrid(L)
		expected += tx * ty
	}
	if stats.Entries != expected {
		t.Errorf("after Generate: entries = %d, want %d", stats.Entries, expected)
	}
}

func TestCancellationNoPollution(t *testing.T) {
	src, _ := NewCheckerSource(256, 256, 4, nrgba(255, 0, 0, 255), nrgba(0, 255, 0, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 8
	cfg.MaxCacheBytes = 1 << 20 // 1 MiB, forces eviction during generation
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- m.Generate(ctx)
	}()
	// Let generation run briefly, then cancel.
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if err != ErrCancelled {
			t.Errorf("Generate after cancel: got %v, want ErrCancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Generate did not return after cancellation")
	}
	// Subsequent requests must work (no pollution).
	tile, err := m.GetTile(context.Background(), 0, 0, 0)
	if err != nil {
		t.Fatalf("GetTile after cancellation: %v", err)
	}
	if tile == nil {
		t.Fatal("GetTile after cancellation returned nil tile")
	}
	// Assembling a level after cancellation must still produce a correct,
	// complete result (tiles regenerate on demand).
	img := assembleLevelRef(t, m, 0)
	if img.Rect.Dx() != 256 || img.Rect.Dy() != 256 {
		t.Errorf("assembled level 0 after cancel: size %dx%d, want 256x256", img.Rect.Dx(), img.Rect.Dy())
	}
}

func TestCancellationDuringGetTile(t *testing.T) {
	src, _ := NewSolidSource(64, 64, nrgba(255, 0, 0, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 8
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_, err = m.GetTile(ctx, 0, 0, 0)
	if err != ErrCancelled {
		t.Errorf("GetTile with cancelled ctx: got %v, want ErrCancelled", err)
	}
}

// --- invalid input ---

func TestInvalidInput(t *testing.T) {
	// Nil source.
	if _, err := NewMipmap(nil, DefaultConfig()); err != ErrInvalidImage {
		t.Errorf("nil source: got %v, want ErrInvalidImage", err)
	}
	// Zero-size source.
	if _, err := NewSolidSource(0, 10, nrgba(0, 0, 0, 255)); err != ErrInvalidImage {
		t.Errorf("zero width: got %v, want ErrInvalidImage", err)
	}
	// Bad tile size.
	src, _ := NewSolidSource(4, 4, nrgba(0, 0, 0, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 0
	if _, err := NewMipmap(src, cfg); err == nil {
		t.Error("tile size 0: expected error")
	}
	// Cache too small for a tile.
	cfg = DefaultConfig()
	cfg.TileSize = 256
	cfg.MaxCacheBytes = 100 // < 256*256*4
	if _, err := NewMipmap(src, cfg); err == nil {
		t.Error("cache too small: expected error")
	}
	// Invalid level.
	m, _ := NewMipmap(src, DefaultConfig())
	if _, err := m.GetTile(context.Background(), 99, 0, 0); err == nil {
		t.Error("invalid level: expected error")
	}
	// Invalid tile coordinate.
	if _, err := m.GetTile(context.Background(), 0, 99, 0); err == nil {
		t.Error("invalid tile: expected error")
	}
}

func TestNewSourceFromImage(t *testing.T) {
	// Build a stdlib image and convert.
	srcImg := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			srcImg.SetNRGBA(x, y, nrgba(10, 20, 30, 255))
		}
	}
	src, err := NewSourceFromImage(srcImg)
	if err != nil {
		t.Fatal(err)
	}
	if src.W != 4 || src.H != 4 {
		t.Errorf("source size = %dx%d, want 4x4", src.W, src.H)
	}
	m, err := NewMipmap(src, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	img := assembleLevelRef(t, m, 0)
	if !imagesEqual(srcImg, img) {
		t.Error("converted source does not match assembled level 0")
	}
}

// --- concurrent stress & bounded memory ---

func TestConcurrentCacheStress(t *testing.T) {
	src, _ := NewCheckerSource(128, 128, 4, nrgba(255, 0, 0, 255), nrgba(0, 255, 0, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 8
	cfg.MaxCacheBytes = 64 * 1024 // small cache to force eviction
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Hammer the cache concurrently from many goroutines.
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				level := i % (m.MaxLevel() + 1)
				gx, gy := m.TileGrid(level)
				tx := (i * 7) % gx
				ty := (i * 13) % gy
				if _, err := m.GetTile(ctx, level, tx, ty); err != nil {
					t.Errorf("GetTile(%d,%d,%d): %v", level, tx, ty, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	// Cache must never exceed its bound.
	stats := m.Stats()
	if stats.Bytes > cfg.MaxCacheBytes {
		t.Errorf("cache bytes %d exceed cap %d", stats.Bytes, cfg.MaxCacheBytes)
	}
	// Hits and misses must both be observable.
	if stats.Hits == 0 {
		t.Error("expected some hits under concurrent stress, got 0")
	}
}

func TestCacheMemoryBoundedUnderLoad(t *testing.T) {
	src, _ := NewSolidSource(256, 256, nrgba(100, 100, 100, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 16
	cfg.MaxCacheBytes = 8 * 1024 // 8 KiB
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Fetch every tile of every level; the cache must stay bounded.
	for L := 0; L <= m.MaxLevel(); L++ {
		tilesX, tilesY := m.TileGrid(L)
		for ty := 0; ty < tilesY; ty++ {
			for tx := 0; tx < tilesX; tx++ {
				if _, err := m.GetTile(ctx, L, tx, ty); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	stats := m.Stats()
	if stats.Bytes > cfg.MaxCacheBytes {
		t.Errorf("cache bytes %d exceed cap %d after full load", stats.Bytes, cfg.MaxCacheBytes)
	}
	if stats.Entries == 0 {
		t.Error("cache should hold some entries")
	}
}

func TestGenerateTimeout(t *testing.T) {
	src, _ := NewCheckerSource(512, 512, 4, nrgba(255, 0, 0, 255), nrgba(0, 255, 0, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 8
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Nanosecond) // ensure deadline exceeded
	err = m.Generate(ctx)
	if err != ErrCancelled {
		t.Errorf("Generate with timeout: got %v, want ErrCancelled", err)
	}
	// Subsequent requests must still work.
	if _, err := m.GetTile(context.Background(), 0, 0, 0); err != nil {
		t.Fatalf("GetTile after timeout: %v", err)
	}
}

func TestGeneratePartialThenComplete(t *testing.T) {
	// Cancel mid-generation, then re-generate to completion. The second
	// generation must succeed and produce a complete mipmap.
	src, _ := NewCheckerSource(128, 128, 4, nrgba(255, 0, 0, 255), nrgba(0, 255, 0, 255))
	cfg := DefaultConfig()
	cfg.TileSize = 8
	m, err := NewMipmap(src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(2 * time.Millisecond)
		cancel()
	}()
	_ = m.Generate(ctx) // may or may not be cancelled
	// Re-generate with a fresh context; must complete.
	if err := m.Generate(context.Background()); err != nil {
		t.Fatalf("re-Generate after cancellation: %v", err)
	}
	// All tiles must be present.
	for L := 0; L <= m.MaxLevel(); L++ {
		tilesX, tilesY := m.TileGrid(L)
		for ty := 0; ty < tilesY; ty++ {
			for tx := 0; tx < tilesX; tx++ {
				if _, err := m.GetTile(context.Background(), L, tx, ty); err != nil {
					t.Errorf("tile (%d,%d,%d) missing after re-Generate: %v", L, tx, ty, err)
				}
			}
		}
	}
}
