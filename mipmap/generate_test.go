package mipmap

import (
	"context"
	"errors"
	"image"
	"image/color"
	"testing"
	"time"
)

// makeTestImage 生成确定性的进程内测试图（NRGBA）。
func makeTestImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i+0] = uint8((x*7 + y*3) % 256)
			img.Pix[i+1] = uint8((x*2 + y*11) % 256)
			img.Pix[i+2] = uint8((x*13 + y*5) % 256)
			img.Pix[i+3] = uint8(128 + (x+y)%128)
		}
	}
	return img
}

// assemble 把某一层全部 Tile 拼接为完整 NRGBA 图。
func assemble(tb testing.TB, p *Pyramid, level int) *image.NRGBA {
	tb.Helper()
	li := p.Level(level)
	img := image.NewNRGBA(image.Rect(0, 0, li.Width, li.Height))
	for ty := 0; ty < li.TileRows; ty++ {
		for tx := 0; tx < li.TileCols; tx++ {
			tile := p.Tile(TileID{Level: level, TX: tx, TY: ty})
			if tile == nil {
				tb.Fatalf("missing tile level=%d (%d,%d)", level, tx, ty)
			}
			for y := 0; y < tile.Rect.Dy(); y++ {
				for x := 0; x < tile.Rect.Dx(); x++ {
					si := (y*tile.Rect.Dx() + x) * 4
					di := img.PixOffset(tile.Rect.Min.X+x, tile.Rect.Min.Y+y)
					copy(img.Pix[di:di+4], tile.Pix[si:si+4])
				}
			}
		}
	}
	return img
}

// TestTilesCoverLevelExactly 验证任一层 Tile 拼接完整：无缺块、无重叠。
func TestTilesCoverLevelExactly(t *testing.T) {
	for _, sz := range [][2]int{{1000, 763}, {1, 1}, {257, 3}, {64, 64}} {
		cfg := DefaultConfig()
		cfg.TileSize = 64
		p, err := Generate(context.Background(), makeTestImage(sz[0], sz[1]), cfg)
		if err != nil {
			t.Fatalf("size %v: %v", sz, err)
		}
		for lv := 0; lv < p.NumLevels(); lv++ {
			li := p.Level(lv)
			cover := make([]int, li.Width*li.Height)
			for ty := 0; ty < li.TileRows; ty++ {
				for tx := 0; tx < li.TileCols; tx++ {
					tile := p.Tile(TileID{Level: lv, TX: tx, TY: ty})
					if tile == nil {
						t.Fatalf("size %v level %d: missing tile (%d,%d)", sz, lv, tx, ty)
					}
					if len(tile.Pix) != tile.Rect.Dx()*tile.Rect.Dy()*4 {
						t.Fatalf("size %v level %d tile (%d,%d): bad pix len", sz, lv, tx, ty)
					}
					for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
						for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
							cover[y*li.Width+x]++
						}
					}
				}
			}
			for i, c := range cover {
				if c != 1 {
					t.Fatalf("size %v level %d pixel %d covered %d times", sz, lv, i, c)
				}
			}
		}
	}
}

// TestLevelSizes 验证层级尺寸按 ceil(n/2) 递减到 1x1。
func TestLevelSizes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TileSize = 32
	p, err := Generate(context.Background(), makeTestImage(100, 53), cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]int{{100, 53}, {50, 27}, {25, 14}, {13, 7}, {7, 4}, {4, 2}, {2, 1}, {1, 1}}
	if p.NumLevels() != len(want) {
		t.Fatalf("NumLevels=%d want %d", p.NumLevels(), len(want))
	}
	for i, w := range want {
		li := p.Level(i)
		if li.Width != w[0] || li.Height != w[1] {
			t.Fatalf("level %d = %dx%d want %dx%d", i, li.Width, li.Height, w[0], w[1])
		}
	}
}

// TestMaxLevels 验证 MaxLevels 截断。
func TestMaxLevels(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TileSize = 32
	cfg.MaxLevels = 3
	p, err := Generate(context.Background(), makeTestImage(100, 53), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.NumLevels() != 3 {
		t.Fatalf("NumLevels=%d want 3", p.NumLevels())
	}
}

// TestDeterministicAcrossTilingAndWorkers 同一源图在任意分块划分与
// 并行度下每一级逐像素一致。
func TestDeterministicAcrossTilingAndWorkers(t *testing.T) {
	src := makeTestImage(300, 217)
	var ref []*image.NRGBA
	for i, cfg := range []Config{
		{TileSize: 16, Workers: 1},
		{TileSize: 64, Workers: 4},
		{TileSize: 128, Workers: 8},
		{TileSize: 300, Workers: 2},
	} {
		p, err := Generate(context.Background(), src, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			for lv := 0; lv < p.NumLevels(); lv++ {
				ref = append(ref, assemble(t, p, lv))
			}
			continue
		}
		if p.NumLevels() != len(ref) {
			t.Fatalf("cfg %d: NumLevels=%d want %d", i, p.NumLevels(), len(ref))
		}
		for lv := 0; lv < p.NumLevels(); lv++ {
			got := assemble(t, p, lv)
			if got.Bounds() != ref[lv].Bounds() {
				t.Fatalf("cfg %d level %d: bounds %v want %v", i, lv, got.Bounds(), ref[lv].Bounds())
			}
			for k := range ref[lv].Pix {
				if got.Pix[k] != ref[lv].Pix[k] {
					t.Fatalf("cfg %d level %d: pixel byte %d differs (%d vs %d)",
						i, lv, k, got.Pix[k], ref[lv].Pix[k])
				}
			}
		}
	}
}

// TestLevel0MatchesSource 第 0 层拼接后必须与源图一致。
func TestLevel0MatchesSource(t *testing.T) {
	src := makeTestImage(130, 71)
	cfg := DefaultConfig()
	cfg.TileSize = 32
	p, err := Generate(context.Background(), src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := assemble(t, p, 0)
	for i := range src.Pix {
		if got.Pix[i] != src.Pix[i] {
			t.Fatalf("byte %d: got %d want %d", i, got.Pix[i], src.Pix[i])
		}
	}
}

// TestSinglePixelImage 单像素图只有一层 1x1。
func TestSinglePixelImage(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	src.SetNRGBA(0, 0, color.NRGBA{10, 20, 30, 200})
	p, err := Generate(context.Background(), src, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if p.NumLevels() != 1 {
		t.Fatalf("NumLevels=%d want 1", p.NumLevels())
	}
	tile := p.Tile(TileID{Level: 0, TX: 0, TY: 0})
	want := []uint8{10, 20, 30, 200}
	for i := range want {
		if tile.Pix[i] != want[i] {
			t.Fatalf("pix %d = %d want %d", i, tile.Pix[i], want[i])
		}
	}
}

// TestAlphaDownsample 验证透明通道的预乘处理：
// 全透明像素的颜色不得污染结果，alpha 按盒式平均。
func TestAlphaDownsample(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	src.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 255}) // 不透明红
	src.SetNRGBA(1, 0, color.NRGBA{0, 0, 255, 255}) // 不透明蓝
	src.SetNRGBA(0, 1, color.NRGBA{0, 0, 0, 0})     // 全透明黑
	src.SetNRGBA(1, 1, color.NRGBA{255, 0, 0, 0})   // 全透明红（不得污染）
	p, err := Generate(context.Background(), src, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	tile := p.Tile(TileID{Level: 1, TX: 0, TY: 0})
	// alpha = (255+255+0+0+2)/4 = 128
	// 预乘平均: pr=pb=(255+2)/4=64, 反预乘 (64*255+64)/128 = 128
	want := []uint8{128, 0, 128, 128}
	for i := range want {
		if tile.Pix[i] != want[i] {
			t.Fatalf("pix = %v want %v", tile.Pix[:4], want)
		}
	}
}

// TestOddSizeEdgeReplication 奇数尺寸下最后一行/列边缘复制。
func TestOddSizeEdgeReplication(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	src.SetNRGBA(0, 0, color.NRGBA{0, 0, 0, 255})
	src.SetNRGBA(1, 0, color.NRGBA{100, 100, 100, 255})
	src.SetNRGBA(2, 0, color.NRGBA{200, 200, 200, 255})
	p, err := Generate(context.Background(), src, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	li := p.Level(1)
	if li.Width != 2 || li.Height != 1 {
		t.Fatalf("level1 = %dx%d want 2x1", li.Width, li.Height)
	}
	tile := p.Tile(TileID{Level: 1, TX: 0, TY: 0})
	// 像素0: (0+100+0+100+2)/4 = 50（y 方向边缘复制）
	if tile.Pix[0] != 50 {
		t.Fatalf("pix0 = %d want 50", tile.Pix[0])
	}
	// 像素1: x=2 边缘复制 -> (200+200+200+200+2)/4 = 200
	if tile.Pix[4] != 200 {
		t.Fatalf("pix1 = %d want 200", tile.Pix[4])
	}
}

// TestGenerateCancel 取消后返回错误且无半成品。
func TestGenerateCancel(t *testing.T) {
	src := makeTestImage(512, 512)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	p, err := Generate(ctx, src, DefaultConfig())
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v want context.Canceled", err)
	}
	if p != nil {
		t.Fatal("cancelled generate must not return a pyramid")
	}
	// 取消不得污染后续请求。
	p2, err := Generate(context.Background(), src, DefaultConfig())
	if err != nil || p2 == nil {
		t.Fatalf("generate after cancel failed: %v", err)
	}
}

// TestGenerateTimeout 超时路径。
func TestGenerateTimeout(t *testing.T) {
	src := makeTestImage(1024, 1024)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	_, err := Generate(ctx, src, DefaultConfig())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v want context.DeadlineExceeded", err)
	}
}

// TestInvalidInputs 异常输入给出可区分错误。
func TestInvalidInputs(t *testing.T) {
	if _, err := Generate(context.Background(), nil, DefaultConfig()); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("nil image: err = %v", err)
	}
	empty := image.NewNRGBA(image.Rectangle{})
	if _, err := Generate(context.Background(), empty, DefaultConfig()); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("empty image: err = %v", err)
	}
	cfg := DefaultConfig()
	cfg.TileSize = -1
	if _, err := Generate(context.Background(), makeTestImage(4, 4), cfg); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("bad tile size: err = %v", err)
	}
	cfg = DefaultConfig()
	cfg.MaxImagePixels = 10
	if _, err := Generate(context.Background(), makeTestImage(100, 100), cfg); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("oversized image: err = %v", err)
	}
}

// TestConcurrentGenerate 并发生成无数据竞争（配合 -race）。
func TestConcurrentGenerate(t *testing.T) {
	src := makeTestImage(256, 256)
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func(i int) {
			ctx := context.Background()
			if i%2 == 1 {
				c, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				ctx = c
			}
			p, err := Generate(ctx, src, DefaultConfig())
			if err == nil && p == nil {
				done <- errors.New("nil pyramid without error")
				return
			}
			done <- nil
		}(i)
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
