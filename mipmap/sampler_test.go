package mipmap

import (
	"context"
	"image"
	"image/color"
	"math"
	"testing"
)

// refBilinear 直接对整图（拼接后的层）做双线性采样，作为无缝性的基准。
// 与 Sampler 使用相同的坐标映射、clamp 规则与预乘插值。
func refBilinear(img *image.NRGBA, x, y float64) (r, g, b, a uint8) {
	w := img.Bounds().Dx()
	h := img.Bounds().Dy()
	at := func(px, py int) color.NRGBA {
		if px < 0 {
			px = 0
		} else if px >= w {
			px = w - 1
		}
		if py < 0 {
			py = 0
		} else if py >= h {
			py = h - 1
		}
		return img.NRGBAAt(px, py)
	}
	x0 := int(math.Floor(x))
	y0 := int(math.Floor(y))
	fx := x - float64(x0)
	fy := y - float64(y0)
	var pr, pg, pb, pa float64
	for j := 0; j <= 1; j++ {
		wy := 1 - fy
		if j == 1 {
			wy = fy
		}
		for i := 0; i <= 1; i++ {
			wx := 1 - fx
			if i == 1 {
				wx = fx
			}
			w := wx * wy
			c := at(x0+i, y0+j)
			af := float64(c.A)
			pr += float64(c.R) * af / 255 * w
			pg += float64(c.G) * af / 255 * w
			pb += float64(c.B) * af / 255 * w
			pa += af * w
		}
	}
	return unpremul(pr, pg, pb, pa)
}

// TestSeamlessAcrossTileBoundary 分块边界附近的插值结果必须与
// 直接对整图插值完全一致。
func TestSeamlessAcrossTileBoundary(t *testing.T) {
	src := makeTestImage(200, 150)
	cfg := DefaultConfig()
	cfg.TileSize = 32
	p, err := Generate(context.Background(), src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSampler(p)
	full := assemble(t, p, 0)
	// 密集扫描所有 Tile 边界附近（含非整数坐标）。
	for bx := 32; bx < 200; bx += 32 {
		for _, off := range []float64{-1.5, -0.75, -0.25, 0, 0.25, 0.75, 1.5} {
			for y := 0.5; y < 150; y += 7.3 {
				x := float64(bx) + off
				gr, gg, gb, ga := s.SampleBilinear(x, y, 0)
				wr, wg, wb, wa := refBilinear(full, x, y)
				if gr != wr || gg != wg || gb != wb || ga != wa {
					t.Fatalf("seam at x=%.2f y=%.2f: got (%d,%d,%d,%d) want (%d,%d,%d,%d)",
						x, y, gr, gg, gb, ga, wr, wg, wb, wa)
				}
			}
		}
	}
	for by := 32; by < 150; by += 32 {
		for _, off := range []float64{-1.5, -0.25, 0.25, 1.5} {
			for x := 0.5; x < 200; x += 9.1 {
				y := float64(by) + off
				gr, gg, gb, ga := s.SampleBilinear(x, y, 0)
				wr, wg, wb, wa := refBilinear(full, x, y)
				if gr != wr || gg != wg || gb != wb || ga != wa {
					t.Fatalf("seam at x=%.2f y=%.2f: got (%d,%d,%d,%d) want (%d,%d,%d,%d)",
						x, y, gr, gg, gb, ga, wr, wg, wb, wa)
				}
			}
		}
	}
}

// TestSeamlessAcrossLevels 每一层的采样都与该层整图插值一致。
func TestSeamlessAcrossLevels(t *testing.T) {
	src := makeTestImage(97, 61)
	cfg := DefaultConfig()
	cfg.TileSize = 16
	p, err := Generate(context.Background(), src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSampler(p)
	for lv := 0; lv < p.NumLevels(); lv++ {
		full := assemble(t, p, lv)
		li := p.Level(lv)
		for y := -1.0; y < float64(li.Height)+1; y += 3.7 {
			for x := -1.0; x < float64(li.Width)+1; x += 2.9 {
				gr, gg, gb, ga := s.bilinearAtLevel(lv, x, y)
				wr, wg, wb, wa := refBilinear(full, x, y)
				if gr != wr || gg != wg || gb != wb || ga != wa {
					t.Fatalf("level %d at (%.2f,%.2f): got (%d,%d,%d,%d) want (%d,%d,%d,%d)",
						lv, x, y, gr, gg, gb, ga, wr, wg, wb, wa)
				}
			}
		}
	}
}

// TestTrilinearSmoothTransition 层级连续变化时输出平滑过渡，
// 不得出现跳变（相邻 level 步进的输出差有界）。
func TestTrilinearSmoothTransition(t *testing.T) {
	src := makeTestImage(128, 128)
	cfg := DefaultConfig()
	cfg.TileSize = 32
	p, err := Generate(context.Background(), src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSampler(p)
	x, y := 64.3, 70.7
	var prev [4]uint8
	first := true
	for lv := 0.0; lv <= float64(p.NumLevels()-1); lv += 0.01 {
		r, g, b, a := s.SampleBilinear(x, y, lv)
		cur := [4]uint8{r, g, b, a}
		if !first {
			for i := 0; i < 4; i++ {
				d := int(cur[i]) - int(prev[i])
				if d < 0 {
					d = -d
				}
				if d > 2 { // 0.01 层级步进下每通道变化不应超过 2
					t.Fatalf("jump at level %.2f channel %d: %d -> %d", lv, i, prev[i], cur[i])
				}
			}
		}
		prev = cur
		first = false
	}
}

// TestLevelClamping 层级越界时截断到有效范围。
func TestLevelClamping(t *testing.T) {
	src := makeTestImage(64, 64)
	p, err := Generate(context.Background(), src, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := NewSampler(p)
	maxL := float64(p.NumLevels() - 1)
	r0, g0, b0, a0 := s.SampleBilinear(10, 10, -5)
	r1, g1, b1, a1 := s.SampleBilinear(10, 10, 0)
	if r0 != r1 || g0 != g1 || b0 != b1 || a0 != a1 {
		t.Fatal("negative level should clamp to 0")
	}
	r0, g0, b0, a0 = s.SampleBilinear(10, 10, 100)
	r1, g1, b1, a1 = s.SampleBilinear(10, 10, maxL)
	if r0 != r1 || g0 != g1 || b0 != b1 || a0 != a1 {
		t.Fatal("oversized level should clamp to max")
	}
}

// TestOutOfRangeCoordClamp 采样坐标越界按边缘复制截断。
func TestOutOfRangeCoordClamp(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	src.SetNRGBA(0, 0, color.NRGBA{9, 8, 7, 255})
	src.SetNRGBA(3, 3, color.NRGBA{1, 2, 3, 255})
	p, err := Generate(context.Background(), src, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := NewSampler(p)
	r, g, b, a := s.SampleBilinear(-100, -100, 0)
	if r != 9 || g != 8 || b != 7 || a != 255 {
		t.Fatalf("corner clamp got (%d,%d,%d,%d)", r, g, b, a)
	}
	r, g, b, a = s.SampleBilinear(1000, 1000, 0)
	if r != 1 || g != 2 || b != 3 || a != 255 {
		t.Fatalf("corner clamp got (%d,%d,%d,%d)", r, g, b, a)
	}
}

// TestAnisoDeterministic 各向异性采样结果确定：重复调用逐像素一致，
// 且与分块划分无关。
func TestAnisoDeterministic(t *testing.T) {
	src := makeTestImage(180, 120)
	var outs [][4]uint8
	for _, tileSize := range []int{16, 64, 256} {
		cfg := DefaultConfig()
		cfg.TileSize = tileSize
		p, err := Generate(context.Background(), src, cfg)
		if err != nil {
			t.Fatal(err)
		}
		s := NewSampler(p)
		var got [][4]uint8
		for y := 10.0; y < 110; y += 13 {
			for x := 10.0; x < 170; x += 11 {
				r, g, b, a := s.SampleAniso(x, y, 8, 1, -1, 4)
				got = append(got, [4]uint8{r, g, b, a})
			}
		}
		if outs == nil {
			outs = got
			continue
		}
		for i := range outs {
			if got[i] != outs[i] {
				t.Fatalf("tileSize=%d sample %d differs: %v vs %v", tileSize, i, got[i], outs[i])
			}
		}
	}
}

// TestAnisoDegenerate 退化足迹（零梯度）不崩溃且等于单点采样。
func TestAnisoDegenerate(t *testing.T) {
	src := makeTestImage(32, 32)
	p, err := Generate(context.Background(), src, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := NewSampler(p)
	r0, g0, b0, a0 := s.SampleAniso(16, 16, 0, 0, 0, 0)
	r1, g1, b1, a1 := s.SampleBilinear(16, 16, 0)
	if r0 != r1 || g0 != g1 || b0 != b1 || a0 != a1 {
		t.Fatal("zero gradient should equal point sample")
	}
}

// TestAnisoSampleCountBounded 采样点数不超过 MaxAniso（通过极端
// 各向异性不超时与不崩溃间接验证，并验证结果有限）。
func TestAnisoSampleCountBounded(t *testing.T) {
	src := makeTestImage(64, 64)
	cfg := DefaultConfig()
	cfg.MaxAniso = 4
	p, err := Generate(context.Background(), src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSampler(p)
	r, g, b, a := s.SampleAniso(32, 32, 1000, 0, 0, 0.001)
	if a == 0 && r == 0 && g == 0 && b == 0 {
		t.Fatal("unexpected transparent black from opaque source")
	}
}
