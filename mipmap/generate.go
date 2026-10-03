package mipmap

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"sync"
)

// Generate 由源图构建完整金字塔。
//
// 确定性：同一源图在任意分块划分、任意并行度、任意生成顺序下，
// 产出的每一级逐像素一致（下采样为纯整数运算，各 Tile 独立计算）。
//
// 取消：ctx 取消或超时时返回包装了 ctx.Err() 的错误，
// 不返回半成品 Pyramid，内部状态全部丢弃，不污染后续调用。
func Generate(ctx context.Context, src image.Image, cfg Config) (*Pyramid, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	if src == nil {
		return nil, fmt.Errorf("%w: nil source image", ErrInvalidImage)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("%w: empty bounds %v", ErrInvalidImage, b)
	}
	if int64(w)*int64(h) > cfg.MaxImagePixels {
		return nil, fmt.Errorf("%w: %dx%d exceeds MaxImagePixels=%d",
			ErrInvalidImage, w, h, cfg.MaxImagePixels)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("mipmap: generate: %w", err)
	}

	levels := computeLevels(w, h, cfg)
	p := &Pyramid{cfg: cfg, levels: levels, tiles: make([][]Tile, len(levels))}
	for i, li := range levels {
		p.tiles[i] = make([]Tile, li.TileCols*li.TileRows)
	}

	// 第 0 层：直接从源图拷贝为 Tile。
	if err := runTiles(ctx, cfg.Workers, 0, levels[0], func(t *Tile) {
		fillTileFromImage(src, b.Min, t)
	}, p.tiles[0], cfg.TileSize); err != nil {
		return nil, err
	}

	// 逐级下采样，源为上一层 Tile 数组（支持跨 Tile halo 读取）。
	for lv := 1; lv < len(levels); lv++ {
		srcTS := tileSourceT{info: levels[lv-1], tiles: p.tiles[lv-1], tileSize: cfg.TileSize}
		if err := runTiles(ctx, cfg.Workers, lv, levels[lv], func(t *Tile) {
			downsampleTile(srcTS, t)
		}, p.tiles[lv], cfg.TileSize); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// runTiles 并行执行一层全部 Tile 的计算，fn 为单 Tile 计算函数。
// 任一 Tile 失败或 ctx 取消时尽快停止并返回错误。
func runTiles(ctx context.Context, workers, level int, li LevelInfo, fn func(t *Tile), tiles []Tile, tileSize int) error {
	total := li.TileCols * li.TileRows
	jobs := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	fail := func(err error) { once.Do(func() { firstErr = err }) }

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if err := ctx.Err(); err != nil {
					fail(fmt.Errorf("mipmap: generate: %w", err))
					continue
				}
				tx := idx % li.TileCols
				ty := idx / li.TileCols
				t := &tiles[idx]
				t.ID = TileID{Level: level, TX: tx, TY: ty}
				t.Rect = li.TileRect(tileSize, tx, ty)
				t.Pix = make([]uint8, t.Rect.Dx()*t.Rect.Dy()*4)
				fn(t)
			}
		}()
	}
loop:
	for i := 0; i < total; i++ {
		select {
		case <-ctx.Done():
			fail(fmt.Errorf("mipmap: generate: %w", ctx.Err()))
			break loop
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()
	return firstErr
}

// fillTileFromImage 把源图对应区域拷贝进 Tile（转为 NRGBA）。
func fillTileFromImage(src image.Image, origin image.Point, t *Tile) {
	w := t.Rect.Dx()
	h := t.Rect.Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(src.At(origin.X+t.Rect.Min.X+x, origin.Y+t.Rect.Min.Y+y)).(color.NRGBA)
			i := (y*w + x) * 4
			t.Pix[i+0] = c.R
			t.Pix[i+1] = c.G
			t.Pix[i+2] = c.B
			t.Pix[i+3] = c.A
		}
	}
}
