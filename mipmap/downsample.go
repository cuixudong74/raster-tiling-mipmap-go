package mipmap

// 下采样规则（确定性，文档见 README）：
//   - 目标像素 (x,y) 对应源层 2x2 块，左上角 (2x,2y)。
//   - 源层尺寸为奇数时，最后一行/列通过边缘复制（clamp）补齐 2x2。
//   - 在预乘 alpha 空间做盒式平均，再反预乘回 NRGBA，
//     避免透明像素的颜色污染（透明通道处理统一）。
//   - 全部使用整数运算（四舍五入），不依赖求和顺序之外的任何状态，
//     因此与分块划分、并行度、生成顺序无关，逐像素确定。

// downsampleTile 计算目标层一块 Tile 的像素。
// src 是上一层图像的只读像素接口（可跨 Tile 访问），输出写入 dst.Pix。
func downsampleTile(src pixelSource, dst *Tile) {
	sw, sh := src.Width(), src.Height()
	w := dst.Rect.Dx()
	h := dst.Rect.Dy()
	ox := dst.Rect.Min.X
	oy := dst.Rect.Min.Y
	for y := 0; y < h; y++ {
		sy0 := 2 * (oy + y)
		sy1 := sy0 + 1
		if sy1 >= sh {
			sy1 = sh - 1 // 边缘复制
		}
		row := y * w * 4
		for x := 0; x < w; x++ {
			sx0 := 2 * (ox + x)
			sx1 := sx0 + 1
			if sx1 >= sw {
				sx1 = sw - 1 // 边缘复制
			}
			r, g, b, a := box2x2(src, sx0, sy0, sx1, sy1)
			i := row + x*4
			dst.Pix[i+0] = r
			dst.Pix[i+1] = g
			dst.Pix[i+2] = b
			dst.Pix[i+3] = a
		}
	}
}

// box2x2 对源层 (x0,y0),(x1,y0),(x0,y1),(x1,y1) 四个像素做
// 预乘 alpha 盒式平均并反预乘，返回 NRGBA。
func box2x2(src pixelSource, x0, y0, x1, y1 int) (r, g, b, a uint8) {
	var pr, pg, pb, pa uint32
	for _, p := range [4][2]int{{x0, y0}, {x1, y0}, {x0, y1}, {x1, y1}} {
		r8, g8, b8, a8 := src.AtNRGBA(p[0], p[1])
		aa := uint32(a8)
		// 预乘（带四舍五入）：p = (c*a + 127) / 255
		pr += (uint32(r8)*aa + 127) / 255
		pg += (uint32(g8)*aa + 127) / 255
		pb += (uint32(b8)*aa + 127) / 255
		pa += aa
	}
	// 平均（带四舍五入）：v = (sum + 2) / 4
	pr = (pr + 2) / 4
	pg = (pg + 2) / 4
	pb = (pb + 2) / 4
	pa = (pa + 2) / 4
	if pa == 0 {
		return 0, 0, 0, 0
	}
	// 反预乘（带四舍五入）：c = (p*255 + a/2) / a
	r8 := (pr*255 + pa/2) / pa
	g8 := (pg*255 + pa/2) / pa
	b8 := (pb*255 + pa/2) / pa
	if r8 > 255 {
		r8 = 255
	}
	if g8 > 255 {
		g8 = 255
	}
	if b8 > 255 {
		b8 = 255
	}
	return uint8(r8), uint8(g8), uint8(b8), uint8(pa)
}

// pixelSource 提供某一层图像的只读像素访问（NRGBA）。
type pixelSource interface {
	// AtNRGBA 返回 (x,y) 处的 NRGBA 像素，坐标保证在图像范围内。
	AtNRGBA(x, y int) (r, g, b, a uint8)
	Width() int
	Height() int
}

// tileSourceT 以某一层已生成的 Tile 数组作为像素来源，
// 支持跨 Tile 边界的邻域读取（下采样 halo）。
type tileSourceT struct {
	info     LevelInfo
	tiles    []Tile
	tileSize int
}

func (ts tileSourceT) Width() int  { return ts.info.Width }
func (ts tileSourceT) Height() int { return ts.info.Height }

func (ts tileSourceT) AtNRGBA(x, y int) (r, g, b, a uint8) {
	tx := x / ts.tileSize
	ty := y / ts.tileSize
	t := &ts.tiles[ty*ts.info.TileCols+tx]
	lx := x - t.Rect.Min.X
	ly := y - t.Rect.Min.Y
	i := (ly*t.Rect.Dx() + lx) * 4
	return t.Pix[i], t.Pix[i+1], t.Pix[i+2], t.Pix[i+3]
}
