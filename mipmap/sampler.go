package mipmap

import "math"

// 采样语义（文档见 README）：
//   - 坐标系：第 0 层像素中心坐标，像素 (i,j) 中心为 (i+0.5, j+0.5)。
//   - 层级映射：level L 的坐标 xL = (x+0.5)/2^L - 0.5。
//   - 越界规则：所有采样坐标按边缘复制（clamp）截断到层范围内。
//   - 插值在预乘 alpha 空间进行，再反预乘回 NRGBA，
//     与下采样的透明通道处理保持一致，块边界无亮度/透明度跳变。
//   - level 为小数时在相邻两层间线性混合（三线性），层级切换平滑。

// Sampler 在金字塔上执行跨分块、跨层级的采样。只读，可并发使用。
type Sampler struct {
	pyr *Pyramid
	cfg Config
}

// NewSampler 基于已生成的金字塔构造采样器。
func NewSampler(p *Pyramid) *Sampler {
	return &Sampler{pyr: p, cfg: p.cfg}
}

// pixelAt 读取 level 层 (x,y) 像素（越界 clamp 到边缘）。
func (s *Sampler) pixelAt(level, x, y int) (r, g, b, a uint8) {
	li := s.pyr.levels[level]
	if x < 0 {
		x = 0
	} else if x >= li.Width {
		x = li.Width - 1
	}
	if y < 0 {
		y = 0
	} else if y >= li.Height {
		y = li.Height - 1
	}
	ts := s.cfg.TileSize
	t := s.pyr.Tile(TileID{Level: level, TX: x / ts, TY: y / ts})
	lx := x - t.Rect.Min.X
	ly := y - t.Rect.Min.Y
	i := (ly*t.Rect.Dx() + lx) * 4
	return t.Pix[i], t.Pix[i+1], t.Pix[i+2], t.Pix[i+3]
}

// bilinearLevel 在 level 层内做双线性采样，坐标为该层的像素中心坐标系
// （可为负或越界，按 clamp 处理）。返回预乘空间的浮点累积值。
func (s *Sampler) bilinearLevel(level int, x, y float64) (pr, pg, pb, pa float64) {
	x0 := int(math.Floor(x))
	y0 := int(math.Floor(y))
	fx := x - float64(x0)
	fy := y - float64(y0)
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
			r, g, b, a := s.pixelAt(level, x0+i, y0+j)
			af := float64(a)
			pr += float64(r) * af / 255 * w
			pg += float64(g) * af / 255 * w
			pb += float64(b) * af / 255 * w
			pa += af * w
		}
	}
	return pr, pg, pb, pa
}

// unpremul 把预乘浮点累积值反预乘为 NRGBA。
func unpremul(pr, pg, pb, pa float64) (r, g, b, a uint8) {
	if pa <= 0 {
		return 0, 0, 0, 0
	}
	clamp := func(v float64) uint8 {
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		return uint8(v + 0.5)
	}
	return clamp(pr * 255 / pa), clamp(pg * 255 / pa), clamp(pb * 255 / pa), clamp(pa)
}

// bilinearAtLevel 在 level 层自身坐标系下做双线性采样并反预乘。
func (s *Sampler) bilinearAtLevel(level int, x, y float64) (r, g, b, a uint8) {
	return unpremul(s.bilinearLevel(level, x, y))
}

// levelCoord 把第 0 层像素中心坐标映射到 level 层坐标。
func levelCoord(v float64, level int) float64 {
	return (v+0.5)/float64(int(1)<<uint(level)) - 0.5
}

// sampleLevel 在第 0 层坐标 (x,y) 处对整数层级 level 做双线性采样。
func (s *Sampler) sampleLevel(x, y float64, level int) (pr, pg, pb, pa float64) {
	maxLevel := s.pyr.NumLevels() - 1
	if level > maxLevel {
		level = maxLevel
	}
	if level < 0 {
		level = 0
	}
	return s.bilinearLevel(level, levelCoord(x, level), levelCoord(y, level))
}

// SampleBilinear 在第 0 层坐标 (x,y) 处按给定层级采样。
// level 允许为小数：在 floor/ceil 两层间线性混合（三线性过渡），
// level 超出范围时截断到 [0, NumLevels-1]。
func (s *Sampler) SampleBilinear(x, y, level float64) (r, g, b, a uint8) {
	maxLevel := float64(s.pyr.NumLevels() - 1)
	if level < 0 {
		level = 0
	}
	if level > maxLevel {
		level = maxLevel
	}
	l0 := int(math.Floor(level))
	l1 := l0 + 1
	frac := level - float64(l0)
	pr0, pg0, pb0, pa0 := s.sampleLevel(x, y, l0)
	if l1 > int(maxLevel) || frac == 0 {
		return unpremul(pr0, pg0, pb0, pa0)
	}
	pr1, pg1, pb1, pa1 := s.sampleLevel(x, y, l1)
	return unpremul(
		pr0*(1-frac)+pr1*frac,
		pg0*(1-frac)+pg1*frac,
		pb0*(1-frac)+pb1*frac,
		pa0*(1-frac)+pa1*frac,
	)
}

// SampleAniso 以第 0 层坐标 (x,y) 为中心做各向异性过滤。
// (dux,duy) 与 (dvx,dvy) 是屏幕空间 u/v 方向移动一像素对应的
// 源图足迹梯度（第 0 层像素单位）。
//
// 规则（确定且可解释）：
//   - 长轴长度 major = max(|du|,|dv|)，短轴 minor = min(|du|,|dv|)。
//   - 层级 lambda = log2(minor)，截断到 [0, NumLevels-1]。
//   - 采样点数 N = clamp(ceil(major/minor), 1, MaxAniso)。
//   - N 个采样点沿长轴方向均匀分布，位置 t_i = (i+0.5)/N - 0.5，
//     权重均为 1/N。minor 为 0 时退化为单点。
//   - 采样点越界时按边缘复制截断。
func (s *Sampler) SampleAniso(x, y, dux, duy, dvx, dvy float64) (r, g, b, a uint8) {
	lenU := math.Hypot(dux, duy)
	lenV := math.Hypot(dvx, dvy)
	major, minor := lenU, lenV
	mx, my := dux, duy
	if lenV > lenU {
		major, minor = lenV, lenU
		mx, my = dvx, dvy
	}
	if minor <= 0 || major <= 0 {
		return s.SampleBilinear(x, y, 0)
	}
	lambda := math.Log2(minor)
	maxLevel := float64(s.pyr.NumLevels() - 1)
	if lambda < 0 {
		lambda = 0
	}
	if lambda > maxLevel {
		lambda = maxLevel
	}
	n := int(math.Ceil(major / minor))
	if n < 1 {
		n = 1
	}
	if n > s.cfg.MaxAniso {
		n = s.cfg.MaxAniso
	}
	var pr, pg, pb, pa float64
	for i := 0; i < n; i++ {
		t := (float64(i)+0.5)/float64(n) - 0.5
		sx := x + mx*t
		sy := y + my*t
		r1, g1, b1, a1 := s.SampleBilinear(sx, sy, lambda)
		af := float64(a1)
		pr += float64(r1) * af / 255
		pg += float64(g1) * af / 255
		pb += float64(b1) * af / 255
		pa += af
	}
	inv := 1 / float64(n)
	return unpremul(pr*inv, pg*inv, pb*inv, pa*inv)
}
